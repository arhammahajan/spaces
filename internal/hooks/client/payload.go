package client

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ReadHookPayload reads one JSON object from stdin without blocking forever.
//
// Claude and Grok can keep the hook's stdin open after writing the payload,
// so this reads whatever arrives, parses as soon as the object is complete,
// and gives up at the deadline. A tty means there is no payload at all.
func ReadHookPayload(timeoutSecs float64) map[string]any {
	out := map[string]any{}
	r := stdin()
	if f, ok := r.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return out
		}
	}
	if timeoutSecs < 0 {
		timeoutSecs = 0
	}
	deadline := time.Now().Add(time.Duration(timeoutSecs * float64(time.Second)))
	type chunk struct {
		data []byte
		eof  bool
	}
	ch := make(chan chunk, 16)
	go func() {
		defer close(ch)
		buf := make([]byte, 65536)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				ch <- chunk{data: cp}
			}
			if err != nil {
				ch <- chunk{eof: true}
				return
			}
		}
	}()
	var data []byte
	tryParse := func() (map[string]any, bool) {
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			return nil, false
		}
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return nil, false
		}
		if m, ok := parsed.(map[string]any); ok {
			return m, true
		}
		return map[string]any{}, true
	}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		poll := 50 * time.Millisecond
		if remaining < poll {
			poll = remaining
		}
		select {
		case c, ok := <-ch:
			if !ok {
				if m, good := tryParse(); good {
					return m
				}
				return out
			}
			if c.eof {
				if m, good := tryParse(); good {
					return m
				}
				return out
			}
			data = append(data, c.data...)
			if m, good := tryParse(); good {
				return m
			}
		case <-time.After(poll):
			if m, good := tryParse(); good {
				return m
			}
		}
	}
	if m, good := tryParse(); good {
		return m
	}
	return out
}

func stdin() io.Reader {
	if StdinValue != nil {
		return StdinValue
	}
	return os.Stdin
}

// ExtractSessionID pulls the conversation id out of a hook payload,
// checking top-level keys then known nested containers, then "id".
func ExtractSessionID(payload map[string]any) string {
	keys := []string{
		"session_id", "sessionId", "sessionID", "thread_id",
		"threadId", "conversation_id", "conversationId",
	}
	for _, k := range keys {
		if v, ok := payload[k].(string); ok && v != "" {
			return v
		}
	}
	for _, c := range []string{"session", "thread", "conversation", "data", "_meta"} {
		if nested, ok := payload[c].(map[string]any); ok {
			for _, k := range keys {
				if v, ok := nested[k].(string); ok && v != "" {
					return v
				}
			}
		}
	}
	if v, ok := payload["id"].(string); ok && v != "" {
		return v
	}
	return ""
}

// reportTTYPaths finds a tty that belongs to the pane, not a daemon's
// detached child. Agents and plugins may run in a server process whose own
// fds are pipes. Walking up to the TUI process still finds the pane's pts,
// so status records land where the pane watcher is listening.
func reportTTYPaths() []string {
	paths := []string{}
	if configured := os.Getenv("SPACES_TTY"); configured != "" {
		paths = append(paths, configured)
	}
	paths = append(paths, "/dev/tty")
	pid := os.Getpid()
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] {
		seen[pid] = true
		if target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/0", pid)); err == nil {
			if strings.HasPrefix(target, "/dev/pts/") || strings.HasPrefix(target, "/dev/tty") {
				paths = append(paths, fmt.Sprintf("/proc/%d/fd/0", pid))
			}
		}
		fields, err := procStatFields(pid)
		if err != nil {
			break
		}
		var ppid int
		if _, err := fmt.Sscanf(strings.Join(fields[1:2], ""), "%d", &ppid); err != nil {
			break
		}
		pid = ppid
	}
	return paths
}

func procStatFields(pid int) ([]string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, err
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return nil, fmt.Errorf("bad stat")
	}
	return strings.Fields(s[i+2:]), nil
}

// EmitProgramStatus writes one OSC 7501 record to the pane's terminal.
// Inside tmux without Zellij it runs dual-layer: it writes the OSC 7501
// sequence to the pane's tty (observed by `tmux-spaces __osc-watch` over
// `pipe-pane -o`) AND projects onto native `@spaces_*` per-pane options as a
// direct fallback when no pipe watcher is attached.
func EmitProgramStatus(state, app, msg, kind string) {
	body := "state=" + state + ":app=" + app
	if kind != "" {
		body += ":kind=" + kind
	}
	if msg != "" {
		body += ":msg=" + b64Encode(msg)
	}
	sequence := "\x1b]7501;" + body + "\x1b\\"
	for _, path := range TtyPathsFunc() {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			continue
		}
		_, werr := f.WriteString(sequence)
		cerr := f.Close()
		if werr == nil && cerr == nil {
			break
		}
	}
	if inTmuxPane() {
		emitTmuxProgramStatus(state, app, msg, kind)
	}
	// Hook stdout is consumed by the agent; never leak protocol bytes there.
}

// EmitPaneTitle sets the pane's OSC 2 title on the terminal.
func EmitPaneTitle(title string) {
	if title == "" {
		return
	}
	sequence := "\x1b]2;" + title + "\x1b\\"
	for _, path := range TtyPathsFunc() {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			continue
		}
		_, werr := f.WriteString(sequence)
		cerr := f.Close()
		if werr == nil && cerr == nil {
			return
		}
	}
}

// PrimeAgentStatus registers the agent as idle after a brief working pulse so
// newly opened or resumed panes appear in attention trackers.
func PrimeAgentStatus(agent string) {
	app := DisplayApp(agent)
	EmitProgramStatus("working", app, "", "")
	EmitProgramStatus("idle", app, "", "")
}

// EmitAgentEvent emits the legacy start/working/done/error/blocked record
// for a hook event name.
func EmitAgentEvent(agent string, event string) {
	if event == "" {
		return
	}
	app := DisplayApp(agent)
	switch event {
	case "start", "resume", "session-start":
		PrimeAgentStatus(agent)
	case "prompt", "working", "busy":
		EmitProgramStatus("working", app, "", "")
	case "stop", "done", "turn-end":
		EmitProgramStatus("done", app, "", "")
	case "error", "failed":
		EmitProgramStatus("error", app, "", "")
	case "blocked", "needs-you", "permission":
		EmitProgramStatus("blocked", app, "", "")
	}
}

// EmitHookStatus projects a normalized hook event onto OSC 7501 and tmux options.
func EmitHookStatus(event map[string]any) {
	if event == nil {
		return
	}
	if truthy(event["child"]) {
		return // child activity must not change the pane's main state
	}
	app, _ := event["app"].(string)
	if app == "" {
		app = "zj"
	}
	raw, _ := event["event"].(string)
	state, _ := event["state"].(string)
	kind := ""
	if state == "blocked" {
		kind, _ = event["kind"].(string)
	}
	text, _ := event["text"].(string)
	if raw == "session_start" {
		EmitProgramStatus("working", app, "", "")
		EmitProgramStatus("idle", app, "", "")
		return
	}
	if raw == "session_end" {
		EmitProgramStatus("idle", app, text, "")
		return
	}
	if inTmuxPane() {
		if title, _ := event["title"].(string); strings.TrimSpace(title) != "" {
			recordTmuxConversationTitle(title)
		}
	}
	if osc, ok := OSCStates[state]; ok && osc != "" {
		EmitProgramStatus(osc, app, text, kind)
	}
}

var paneIDDigits = regexp.MustCompile(`(\d+)`)

var paneIntRe = regexp.MustCompile(`^[+-]?[0-9]+$`)

// paneInt parses a pane id the way Python's int() does: surrounding
// whitespace tolerated, leading zeros and +/- accepted.
func paneInt(s string) (int, bool) {
	trimmed := strings.TrimSpace(s)
	if !paneIntRe.MatchString(trimmed) {
		return 0, false
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, false
	}
	return n, true
}

// HookTarget returns the current (session, pane id) from Zellij or tmux.
func HookTarget() (string, string, bool) {
	return hookTarget()
}

// hookTarget returns the (session, pane id) this hook attributes to.
// With ZELLIJ_SESSION_NAME set the Zellij pane ids win; otherwise the tmux
// environment (TMUX + TMUX_PANE) is resolved.
func hookTarget() (string, string, bool) {
	session := os.Getenv("ZELLIJ_SESSION_NAME")
	paneRaw := os.Getenv("ZELLIJ_PANE_ID")
	m := paneIDDigits.FindString(paneRaw)
	if session != "" {
		if m == "" {
			return "", "", false
		}
		var pane int
		if _, err := fmt.Sscanf(m, "%d", &pane); err != nil {
			return "", "", false
		}
		return session, strconv.Itoa(pane), true
	}
	return hookTargetTmux()
}

// RecordResumeTarget stores the conversation id on the current tmux pane.
func RecordResumeTarget(agent string, payload map[string]any) {
	sessionID := ExtractSessionID(payload)
	if sessionID == "" {
		return
	}
	if inTmuxPane() {
		recordTmuxResumeTarget(agent, sessionID)
	}
}
