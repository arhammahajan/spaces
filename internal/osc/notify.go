package osc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Notification describes a terminal desktop notification emitted when a coding
// agent finishes a turn, fails, or needs user input.
type Notification struct {
	Session string
	Pane    string
	Agent   string
	App     string
	State   string // "done", "failed", "blocked"
	Title   string
	Body    string
}

// TmuxClient describes one attached tmux client TTY.
type TmuxClient struct {
	TTY      string
	TermName string
	Session  string
	PaneID   string
}

// ListTmuxClientsFunc lists currently attached tmux clients. Tests may override it.
var ListTmuxClientsFunc = defaultListTmuxClients

// WriteTTYFunc writes an escape sequence to a client TTY path. Tests may override it.
var WriteTTYFunc = defaultWriteTTY

func sanitizeOSCField(s string, maxRunes int) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range strings.TrimSpace(s) {
		if r < 0x20 || r == 0x7f {
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		if r == ';' {
			b.WriteByte(',')
			lastSpace = false
			continue
		}
		b.WriteRune(r)
		lastSpace = (r == ' ')
	}
	out := strings.TrimSpace(b.String())
	if maxRunes > 0 {
		runes := []rune(out)
		if len(runes) > maxRunes {
			out = strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
		}
	}
	return out
}

// FormatNotification formats a terminal notification escape sequence for
// termName. Kitty terminals receive OSC 99 (structured title + body); all
// other terminals receive OSC 9 (supported by Ghostty, WezTerm, iTerm2, Foot,
// and Windows Terminal).
func FormatNotification(termName string, n Notification) string {
	app := sanitizeOSCField(n.App, 32)
	if app == "" {
		app = sanitizeOSCField(n.Agent, 32)
	}
	if app == "" {
		app = "Agent"
	}

	var verb string
	switch strings.ToLower(strings.TrimSpace(n.State)) {
	case "failed", "error":
		verb = "failed"
	case "blocked", "waiting":
		verb = "needs input"
	default:
		verb = "finished"
	}

	header := app + " " + verb
	if sess := sanitizeOSCField(n.Session, 32); sess != "" {
		header += " · " + sess
	}

	title := sanitizeOSCField(n.Title, 96)
	body := sanitizeOSCField(n.Body, 140)
	var detail string
	switch {
	case title != "" && body != "" && !strings.EqualFold(title, body) &&
		!strings.EqualFold(body, "turn complete") && !strings.EqualFold(body, "finished"):
		detail = title + " — " + body
	case title != "":
		detail = title
	default:
		detail = body
	}
	detail = sanitizeOSCField(detail, 160)

	if strings.Contains(strings.ToLower(termName), "kitty") {
		id := sanitizeOSCField(n.Agent+"-"+strings.TrimPrefix(n.Pane, "%"), 32)
		if id == "" {
			id = "spaces"
		}
		if detail != "" {
			return fmt.Sprintf("\x1b]99;i=%s:d=0:p=title;%s\x1b\\\x1b]99;i=%s:d=1:p=body;%s\x1b\\", id, header, id, detail)
		}
		return fmt.Sprintf("\x1b]99;i=%s:d=1:p=title;%s\x1b\\", id, header)
	}

	if detail != "" {
		return fmt.Sprintf("\x1b]9;%s: %s\x1b\\", header, detail)
	}
	return fmt.Sprintf("\x1b]9;%s\x1b\\", header)
}

// SendTmuxNotification delivers a terminal notification to all attached tmux
// client TTYs so notifications work for every coding agent across all sessions
// and background tabs. Disabled when TMUX_SPACES_NOTIFY=0.
func SendTmuxNotification(n Notification) {
	if strings.TrimSpace(os.Getenv("TMUX_SPACES_NOTIFY")) == "0" {
		return
	}
	if ListTmuxClientsFunc == nil || WriteTTYFunc == nil {
		return
	}
	clients := ListTmuxClientsFunc()
	if len(clients) == 0 {
		return
	}
	wantPane := strings.TrimPrefix(strings.TrimSpace(n.Pane), "%")
	isCodexDone := strings.EqualFold(strings.TrimSpace(n.Agent), "codex") &&
		strings.EqualFold(strings.TrimSpace(n.State), "done")

	for _, c := range clients {
		if strings.TrimSpace(c.TTY) == "" {
			continue
		}
		// Codex's own TUI emits a native terminal notification when its pane
		// is the active pane of the attached session; skip duplicate delivery
		// on that active client while still notifying for background panes.
		if isCodexDone && wantPane != "" &&
			c.Session == n.Session && strings.TrimPrefix(c.PaneID, "%") == wantPane {
			continue
		}
		seq := FormatNotification(c.TermName, n)
		if seq != "" {
			_ = WriteTTYFunc(c.TTY, seq)
		}
	}
}

func defaultListTmuxClients() []TmuxClient {
	bin := strings.TrimSpace(os.Getenv("TMUX_BIN"))
	if bin == "" {
		bin = "tmux"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "list-clients", "-F", "#{client_tty}\x1f#{client_termname}\x1f#{client_session}\x1f#{pane_id}").Output()
	if err != nil {
		return nil
	}
	var clients []TmuxClient
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 4 {
			continue
		}
		tty := strings.TrimSpace(parts[0])
		if tty == "" {
			continue
		}
		clients = append(clients, TmuxClient{
			TTY:      tty,
			TermName: strings.TrimSpace(parts[1]),
			Session:  strings.TrimSpace(parts[2]),
			PaneID:   strings.TrimSpace(parts[3]),
		})
	}
	return clients
}

func defaultWriteTTY(tty, seq string) error {
	f, err := os.OpenFile(tty, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(seq)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
