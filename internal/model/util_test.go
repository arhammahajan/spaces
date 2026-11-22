package model

import (
	"testing"
)

func TestIsShell(t *testing.T) {
	for _, sh := range []string{"sh", "bash", "zsh", "fish", "nu", "nushell", "dash", "ksh", "tcsh", "csh", "Bash", " ZSH "} {
		if !IsShell(sh) {
			t.Errorf("IsShell(%q) = false", sh)
		}
	}
	for _, other := range []string{"", "opencode", "nvim", "shell", "bashful"} {
		if IsShell(other) {
			t.Errorf("IsShell(%q) = true", other)
		}
	}
}

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"opencode --auto":  "opencode",
		"/usr/bin/pi":      "pi",
		"/bin/bash -l":     "bash",
		"  nvim  file.txt": "nvim",
		"":                 "",
		"   ":              "",
		"/usr/local/bin/":  "bin",
	}
	for in, want := range cases {
		if got := CommandName(in); got != want {
			t.Errorf("CommandName(%q) = %q want %q", in, got, want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	cases := map[int64]string{
		-5: "now", 0: "now", 4_999: "now",
		12_000: "12s", 59_999: "59s",
		5 * 60_000:           "5m",
		3 * 60 * 60_000:      "3h",
		2 * 24 * 60 * 60_000: "2d",
	}
	for in, want := range cases {
		if got := FormatAge(in); got != want {
			t.Errorf("FormatAge(%d) = %q want %q", in, got, want)
		}
	}
}

func TestShortenHome(t *testing.T) {
	if got := ShortenHome("/home/me/dev", "/home/me"); got != "~/dev" {
		t.Errorf("got %q", got)
	}
	if got := ShortenHome("/home/me", "/home/me"); got != "~" {
		t.Errorf("got %q", got)
	}
	if got := ShortenHome("/etc/hosts", "/home/me"); got != "/etc/hosts" {
		t.Errorf("got %q", got)
	}
	if got := ShortenHome("/home/other/x", "/home/me"); got != "/home/other/x" {
		t.Errorf("prefix sibling must not shorten, got %q", got)
	}
	if got := ShortenHome("/a/b", ""); got != "/a/b" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeSpaceName(t *testing.T) {
	if got := SanitizeSpaceName(" dots "); got != "dots" {
		t.Errorf("got %q", got)
	}
	for _, bad := range []string{"", "   ", "a/b", "a\x01b", "a\nb"} {
		if got := SanitizeSpaceName(bad); got != "" {
			t.Errorf("SanitizeSpaceName(%q) = %q want empty", bad, got)
		}
	}
}

func TestIsKnownAgentAndPaneIsAgent(t *testing.T) {
	for _, a := range []string{"claude", "claude-code", "codex", "opencode", "gemini", "cursor-agent", "herdr:pi", "/usr/local/bin/kimi-cli"} {
		if !IsKnownAgent(a) {
			t.Errorf("IsKnownAgent(%q) = false", a)
		}
	}
	for _, non := range []string{"", "bash", "zsh", "shell", "terminal", "nvim", "htop"} {
		if IsKnownAgent(non) {
			t.Errorf("IsKnownAgent(%q) = true", non)
		}
	}
	if !(Pane{App: "claude"}).IsAgent() {
		t.Errorf("known agent pane should be IsAgent")
	}
	if !(Pane{App: "custom-bot", HasSpacesMeta: true}).IsAgent() {
		t.Errorf("pane with spaces metadata should be IsAgent")
	}
	if !(Pane{App: "node", Hook: &HookSummary{Agent: "claude"}}).IsAgent() {
		t.Errorf("pane with hook should be IsAgent")
	}
	if (Pane{App: "shell", Command: "zsh"}).IsAgent() {
		t.Errorf("shell pane should not be IsAgent")
	}
	if (Pane{App: "claude", IsDead: true, Status: StatusIdle}).IsAgent() {
		t.Errorf("dead idle pane should not be IsAgent")
	}
	if !(Pane{App: "claude", IsDead: true, Status: StatusFailed}).IsAgent() {
		t.Errorf("dead failed pane should remain IsAgent")
	}
}
