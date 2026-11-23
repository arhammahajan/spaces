package osc

import (
	"strings"
	"testing"
)

func TestFormatNotificationKittyAndGeneric(t *testing.T) {
	n := Notification{
		Session: "spaces",
		Pane:    "%2",
		Agent:   "antigravity",
		App:     "Antigravity",
		State:   "done",
		Title:   "Fix codex tracking",
		Body:    "turn complete",
	}

	kitty := FormatNotification("xterm-kitty", n)
	wantKitty := "\x1b]99;i=antigravity-2:d=0:p=title;Antigravity finished · spaces\x1b\\\x1b]99;i=antigravity-2:d=1:p=body;Fix codex tracking\x1b\\"
	if kitty != wantKitty {
		t.Fatalf("kitty seq = %q, want %q", kitty, wantKitty)
	}

	generic := FormatNotification("xterm-256color", n)
	wantGeneric := "\x1b]9;Antigravity finished · spaces: Fix codex tracking\x1b\\"
	if generic != wantGeneric {
		t.Fatalf("generic seq = %q, want %q", generic, wantGeneric)
	}

	blocked := FormatNotification("xterm-kitty", Notification{
		Session: "dev",
		Pane:    "5",
		Agent:   "claude",
		App:     "Claude Code",
		State:   "blocked",
		Body:    "Allow bash command?\x1b[31m;now",
	})
	if strings.Contains(blocked, "\x1b[31m") {
		t.Fatalf("control chars were not sanitized: %q", blocked)
	}
	if !strings.Contains(blocked, "Claude Code needs input · dev") {
		t.Fatalf("unexpected blocked header: %q", blocked)
	}
}

func TestSendTmuxNotificationDeliversAcrossAgentsAndAvoidsActiveCodexDuplicate(t *testing.T) {
	oldList, oldWrite := ListTmuxClientsFunc, WriteTTYFunc
	t.Cleanup(func() {
		ListTmuxClientsFunc, WriteTTYFunc = oldList, oldWrite
	})

	ListTmuxClientsFunc = func() []TmuxClient {
		return []TmuxClient{
			{TTY: "/dev/pts/0", TermName: "xterm-kitty", Session: "dev", PaneID: "%20"},
		}
	}

	var sent []struct{ tty, seq string }
	WriteTTYFunc = func(tty, seq string) error {
		sent = append(sent, struct{ tty, seq string }{tty, seq})
		return nil
	}

	// Non-Codex agent in background session notifies attached Kitty client.
	SendTmuxNotification(Notification{
		Session: "spaces", Pane: "2", Agent: "antigravity", App: "Antigravity",
		State: "done", Title: "Done task",
	})
	if len(sent) != 1 || sent[0].tty != "/dev/pts/0" || !strings.Contains(sent[0].seq, "Antigravity finished · spaces") {
		t.Fatalf("expected notification for antigravity, got %+v", sent)
	}

	// Codex in active pane of attached session skips duplicate (Codex TUI already notified).
	sent = nil
	SendTmuxNotification(Notification{
		Session: "dev", Pane: "20", Agent: "codex", App: "Codex",
		State: "done", Title: "Active codex",
	})
	if len(sent) != 0 {
		t.Fatalf("expected active-pane codex done to skip duplicate, got %+v", sent)
	}

	// Codex in a background pane DOES notify.
	SendTmuxNotification(Notification{
		Session: "harnesses", Pane: "4", Agent: "codex", App: "Codex",
		State: "done", Title: "Background codex",
	})
	if len(sent) != 1 || !strings.Contains(sent[0].seq, "Codex finished · harnesses") {
		t.Fatalf("expected background codex notification, got %+v", sent)
	}

	// Disabled via TMUX_SPACES_NOTIFY=0.
	t.Setenv("TMUX_SPACES_NOTIFY", "0")
	sent = nil
	SendTmuxNotification(Notification{
		Session: "spaces", Pane: "2", Agent: "claude", App: "Claude Code", State: "done",
	})
	if len(sent) != 0 {
		t.Fatalf("expected opt-out to suppress notifications, got %+v", sent)
	}
}
