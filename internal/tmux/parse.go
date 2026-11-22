package tmux

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// unitSep is the ASCII unit separator used in tmux -F format strings so that
// spaces and tabs inside titles, paths, and messages never break parsing.
const unitSep = "\x1f"

// ErrNoServer reports that no tmux server is running (or the target session
// does not exist). Callers can test for it with errors.Is.
var ErrNoServer = errors.New("tmux server not running")

// noServerNeedles are matched (case-insensitively) against stderr and error
// text to translate tmux failures into ErrNoServer.
var noServerNeedles = []string{
	"no server running",
	"no server found",
	"failed to connect to server",
	"can't find session",
	"couldn't find session",
	"no current session",
	"no sessions",
}

// isNoServer reports whether err/standard-error text means "no tmux server".
func isNoServer(stderr string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoServer) {
		return true
	}
	hay := strings.ToLower(stderr + "\x00" + err.Error())
	for _, needle := range noServerNeedles {
		if strings.Contains(hay, needle) {
			return true
		}
	}
	return false
}

// commandError decorates a runner failure with the tmux argv and stderr,
// translating "no server" failures into ErrNoServer.
func commandError(args []string, stderr string, exitCode int, err error) error {
	detail := strings.TrimSpace(stderr)
	base := fmt.Sprintf("tmux %s (exit %d)", strings.Join(args, " "), exitCode)
	if detail != "" {
		base += ": " + detail
	}
	if isNoServer(stderr, err) {
		return fmt.Errorf("%s: %w", base, ErrNoServer)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", base, err)
	}
	return errors.New(base)
}

// splitOutputLines splits tmux stdout into non-empty lines.
func splitOutputLines(stdout string) []string {
	var lines []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// splitFields splits one \x1f-separated tmux -F record.
func splitFields(line string) []string {
	return strings.Split(line, unitSep)
}

func parseTmuxBool(s string) bool {
	return strings.TrimSpace(s) == "1"
}

func parseTmuxInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func parseTmuxInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// spacesValue normalizes a native @spaces_* option: unset options expand to
// "" (and defensive "-" placeholders are treated as empty too).
func spacesValue(s string) string {
	if s == "" || s == "-" {
		return ""
	}
	return s
}

// ParseNumericPaneID converts a tmux pane id like "%12" to 12.
// It returns -1 for malformed ids (pane numbers are never negative).
func ParseNumericPaneID(id string) int {
	s := strings.TrimSpace(id)
	s = strings.TrimPrefix(s, "%")
	if s == "" {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// parseSessionRecord parses one list-sessions -F record.
func parseSessionRecord(line string) (Session, bool) {
	fields := splitFields(line)
	if len(fields) < 5 {
		return Session{}, false
	}
	var created time.Time
	if secs, err := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64); err == nil && secs > 0 {
		created = time.Unix(secs, 0)
	}
	s := Session{
		ID:        fields[0],
		Name:      fields[1],
		Attached:  parseTmuxInt(fields[2]),
		CreatedAt: created,
		Windows:   parseTmuxInt(fields[4]),
	}
	if len(fields) > 5 {
		s.WorktreePath = spacesValue(fields[5])
	}
	if len(fields) > 6 {
		s.WorktreeName = spacesValue(fields[6])
	}
	if len(fields) > 7 {
		s.WorktreeBase = spacesValue(fields[7])
	}
	if len(fields) > 8 {
		s.WorktreeManaged = parseTmuxBool(fields[8])
	}
	return s, true
}

// parseWindowRecord parses one list-windows -F record.
func parseWindowRecord(line string) (Window, bool) {
	fields := splitFields(line)
	if len(fields) < 8 {
		return Window{}, false
	}
	w := Window{
		SessionName: fields[0],
		ID:          fields[1],
		Index:       parseTmuxInt(fields[2]),
		Name:        fields[3],
		Active:      parseTmuxBool(fields[4]),
		Bell:        parseTmuxBool(fields[5]),
		Activity:    parseTmuxBool(fields[6]),
		Silence:     parseTmuxBool(fields[7]),
	}
	if len(fields) > 8 {
		w.Layout = fields[8]
	}
	if len(fields) > 9 {
		w.Last = parseTmuxBool(fields[9])
	}
	if len(fields) > 10 {
		w.Zoomed = parseTmuxBool(fields[10])
	}
	if len(fields) > 11 {
		w.Width = parseTmuxInt(fields[11])
	}
	if len(fields) > 12 {
		w.Height = parseTmuxInt(fields[12])
	}
	if len(fields) > 13 {
		if secs, err := strconv.ParseInt(strings.TrimSpace(fields[13]), 10, 64); err == nil && secs > 0 {
			w.ActivityAt = time.Unix(secs, 0)
		}
	}
	if len(fields) > 14 {
		w.WorktreePath = spacesValue(fields[14])
	}
	if len(fields) > 15 {
		w.WorktreeName = spacesValue(fields[15])
	}
	if len(fields) > 16 {
		w.WorktreeBase = spacesValue(fields[16])
	}
	if len(fields) > 17 {
		w.WorktreeManaged = parseTmuxBool(fields[17])
	}
	return w, true
}

// paneFormat is the -F format for ListPanes. Field order must match
// parsePaneRecord.
const paneFormat = "#{session_name}\x1f#{window_id}\x1f#{window_index}\x1f" +
	"#{window_name}\x1f#{pane_id}\x1f#{pane_title}\x1f#{pane_current_command}\x1f" +
	"#{pane_current_path}\x1f#{pane_pid}\x1f#{pane_active}\x1f#{window_active}\x1f" +
	"#{pane_dead}\x1f#{window_bell_flag}\x1f#{window_activity_flag}\x1f" +
	"#{window_silence_flag}\x1f#{@spaces_state}\x1f#{@spaces_app}\x1f" +
	"#{@spaces_kind}\x1f#{@spaces_msg}\x1f#{@spaces_ts}\x1f" +
	"#{pane_index}\x1f#{@spaces_resume_session}\x1f#{@spaces_profile}\x1f#{@spaces_source}\x1f#{@spaces_title}\x1f" +
	"#{pane_width}\x1f#{pane_height}\x1f#{pane_in_mode}\x1f#{pane_synchronized}\x1f#{pane_start_command}\x1f#{pane_dead_status}"

// paneFieldCount is the minimum number of \x1f-separated fields ListPanes expects.
const paneFieldCount = 20

// parsePaneRecord parses one list-panes -F record.
func parsePaneRecord(line string) (Pane, bool) {
	fields := splitFields(line)
	if len(fields) < paneFieldCount {
		return Pane{}, false
	}
	p := Pane{
		SessionName:    fields[0],
		WindowID:       fields[1],
		WindowIndex:    parseTmuxInt(fields[2]),
		WindowName:     fields[3],
		ID:             fields[4],
		NumericID:      ParseNumericPaneID(fields[4]),
		Title:          fields[5],
		CurrentCommand: fields[6],
		CurrentPath:    fields[7],
		PID:            parseTmuxInt(fields[8]),
		Active:         parseTmuxBool(fields[9]),
		WindowActive:   parseTmuxBool(fields[10]),
		Dead:           parseTmuxBool(fields[11]),
		WindowBell:     parseTmuxBool(fields[12]),
		WindowActivity: parseTmuxBool(fields[13]),
		WindowSilence:  parseTmuxBool(fields[14]),
		SpacesState:    spacesValue(fields[15]),
		SpacesApp:      spacesValue(fields[16]),
		SpacesKind:     spacesValue(fields[17]),
		SpacesMsg:      spacesValue(fields[18]),
		SpacesTsMs:     parseTmuxInt64(fields[19]),
	}
	if len(fields) > 20 {
		p.PaneIndex = parseTmuxInt(fields[20])
	}
	if len(fields) > 21 {
		p.ResumeSession = spacesValue(fields[21])
	}
	if len(fields) > 22 {
		p.ResumeProfile = spacesValue(fields[22])
	}
	if len(fields) > 23 {
		p.SpacesSource = spacesValue(fields[23])
	}
	if len(fields) > 24 {
		p.SpacesTitle = spacesValue(fields[24])
	}
	if len(fields) > 25 {
		p.Width = parseTmuxInt(fields[25])
	}
	if len(fields) > 26 {
		p.Height = parseTmuxInt(fields[26])
	}
	if len(fields) > 27 {
		p.InMode = parseTmuxBool(fields[27])
	}
	if len(fields) > 28 {
		p.Synchronized = parseTmuxBool(fields[28])
	}
	if len(fields) > 29 {
		p.StartCommand = strings.TrimSpace(fields[29])
	}
	if len(fields) > 30 {
		p.DeadStatus = parseTmuxInt(fields[30])
	}
	return p, true
}
