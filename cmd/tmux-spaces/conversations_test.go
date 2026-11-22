package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/tmux"
	"github.com/arhammahajan/spaces/internal/tui"
)

func TestParseConversationsOptions(t *testing.T) {
	opts := parseConversationsOptions([]string{"--query", "@claude rate limit"})
	if opts.InitialMode != tui.ModeConversations || opts.InitialQuery != "@claude rate limit" {
		t.Fatalf("unexpected opts: %+v", opts)
	}
	opts2 := parseConversationsOptions([]string{"--query=backoff"})
	if opts2.InitialMode != tui.ModeConversations || opts2.InitialQuery != "backoff" {
		t.Fatalf("unexpected opts2: %+v", opts2)
	}
}

func TestBuildConversationOpenCommandHarnessAndFallback(t *testing.T) {
	// Harness-managed agent with explicit profile uses native resume flags under __shim.
	cmd := buildConversationOpenCommand("claude", "work", "sess-123", "/repo", "Fix retries", "/usr/local/bin/tmux-spaces", "/usr/local/bin/harness")
	joined := strings.Join(cmd, " ")
	for _, want := range []string{
		"/bin/sh -c",
		"/usr/local/bin/tmux-spaces __shim --profile work claude-code /usr/local/bin/harness claude profile work --resume sess-123",
		"status clear",
		`exec "${SHELL:-/bin/sh}"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("harness cmd missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "__resume-wrap") {
		t.Errorf("harness cmd should not use __resume-wrap: %s", joined)
	}

	// Standalone fallback when harness is unavailable uses direct binary + native resume args.
	cmdStandalone := buildConversationOpenCommand("claude", "", "sess-456", "/repo", "Fix retries", "/usr/local/bin/tmux-spaces", "")
	joinedStandalone := strings.Join(cmdStandalone, " ")
	if !strings.Contains(joinedStandalone, "/usr/local/bin/tmux-spaces __shim claude-code claude --resume sess-456") {
		t.Errorf("standalone claude cmd = %s", joinedStandalone)
	}

	// Non-harness agent (omp) launches directly under __shim without __resume-wrap.
	cmdOmp := buildConversationOpenCommand("omp", "", "omp-sess-7", "/repo", "Omp chat", "/usr/local/bin/tmux-spaces", "/usr/local/bin/harness")
	joinedOmp := strings.Join(cmdOmp, " ")
	for _, want := range []string{"/usr/local/bin/tmux-spaces __shim omp omp", "status clear", `exec "${SHELL:-/bin/sh}"`} {
		if !strings.Contains(joinedOmp, want) {
			t.Errorf("omp fallback cmd missing %q: %s", want, joinedOmp)
		}
	}
	if strings.Contains(joinedOmp, "__resume-wrap") {
		t.Errorf("omp cmd should not use __resume-wrap: %s", joinedOmp)
	}

	// Non-harness agent (crush) launches directly under __shim without __resume-wrap.
	cmdCrush := buildConversationOpenCommand("crush", "", "crush-9", "/repo", "Crush chat", "/usr/local/bin/tmux-spaces", "/usr/local/bin/harness")
	joinedCrush := strings.Join(cmdCrush, " ")
	for _, want := range []string{"/usr/local/bin/tmux-spaces __shim crush crush", "status clear", `exec "${SHELL:-/bin/sh}"`} {
		if !strings.Contains(joinedCrush, want) {
			t.Errorf("crush fallback cmd missing %q: %s", want, joinedCrush)
		}
	}
	if strings.Contains(joinedCrush, "__resume-wrap") {
		t.Errorf("crush cmd should not use __resume-wrap: %s", joinedCrush)
	}
}

func TestTUIBackendOpenConversationTargetsCurrentSessionAndRecords(t *testing.T) {
	tmpHome := t.TempDir()
	overlayPath := filepath.Join(tmpHome, "conversations-overlay.json")
	origHome := model.ConversationHomeDirFn
	origOverlay := model.ConversationOverlayPathFn
	model.ConversationHomeDirFn = func() string { return tmpHome }
	model.ConversationOverlayPathFn = func() string { return overlayPath }
	model.ResetConversationStoreCache()
	defer func() {
		model.ConversationHomeDirFn = origHome
		model.ConversationOverlayPathFn = origOverlay
		model.ResetConversationStoreCache()
	}()

	var newWindows [][]string
	var switchedTo []string
	curSession := "alpha"
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "display-message":
				if curSession == "" {
					return "", "no client", 1, fmt.Errorf("no current session")
				}
				return curSession + "\n", "", 0, nil
			case "list-sessions":
				return "$0\x1falpha\x1f1\x1f1700000000\x1f1\n$1\x1fbeta\x1f0\x1f1700000000\x1f1\n", "", 0, nil
			case "new-window":
				newWindows = append(newWindows, append([]string(nil), args...))
				return "", "", 0, nil
			case "switch-client":
				switchedTo = append(switchedTo, args[len(args)-1])
				return "", "", 0, nil
			}
			return "", "", 0, nil
		},
	}

	client := tmux.NewClient(runner)
	client.Getenv = func(k string) string {
		if k == "TMUX" {
			return "/tmp/tmux-1000/default,1,0"
		}
		return ""
	}
	b := newTUIBackend(client)
	b.selfPath = "/usr/local/bin/tmux-spaces"
	b.now = func() time.Time { return time.UnixMilli(1700000555000) }
	b.dirExists = func(p string) bool { return p == "/repo/work" }
	b.lookPath = func(bin string) (string, error) {
		if bin == "harness" {
			return "/usr/local/bin/harness", nil
		}
		return "", fmt.Errorf("not found")
	}

	// Opens in CURRENT tmux session ("alpha") even if conv.Space was "beta",
	// uses ResumeTarget when set, and records the open event.
	err := b.OpenConversation(context.Background(), model.ConversationEntry{
		Agent:        "claude",
		ID:           "conv-id-1",
		ResumeTarget: "resume-target-1",
		App:          "claude-code",
		Profile:      "work",
		Title:        "Gateway retries",
		CWD:          "/repo/work",
		Space:        "beta",
	}, false)
	if err != nil {
		t.Fatalf("OpenConversation error: %v", err)
	}
	if len(newWindows) != 1 {
		t.Fatalf("expected 1 new-window call, got %d", len(newWindows))
	}
	joined := strings.Join(newWindows[0], " ")
	for _, want := range []string{"-t alpha:", "-c /repo/work", "-n work", "resume-target-1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("new-window args missing %q: %v", want, newWindows[0])
		}
	}
	if len(switchedTo) != 0 {
		t.Errorf("should not switch-client when opening in current session alpha, got %v", switchedTo)
	}
	rawOverlay, err := os.ReadFile(overlayPath)
	if err != nil {
		t.Fatalf("expected native overlay file at %s: %v", overlayPath, err)
	}
	var overlay struct {
		Entries map[string]struct {
			SessionID string `json:"session_id"`
			Space     string `json:"space"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rawOverlay, &overlay); err != nil {
		t.Fatalf("unmarshal overlay: %v", err)
	}
	rec, ok := overlay.Entries["claude:conv-id-1"]
	if !ok || rec.SessionID != "conv-id-1" || rec.Space != "alpha" {
		t.Errorf("unexpected recorded overlay payload: %s", string(rawOverlay))
	}

	// When CurrentSession is unavailable, falls back to conv.Space ("beta"), skips missing CWD, and switches to beta.
	curSession = ""
	err = b.OpenConversation(context.Background(), model.ConversationEntry{
		Agent: "codex",
		ID:    "conv-id-2",
		App:   "codex",
		CWD:   "/missing/dir",
		Space: "beta",
	}, true)
	if err != nil {
		t.Fatalf("OpenConversation fallback error: %v", err)
	}
	if len(newWindows) != 2 {
		t.Fatalf("expected 2 new-window calls, got %d", len(newWindows))
	}
	joined2 := strings.Join(newWindows[1], " ")
	wantDefaultCmd := "/usr/local/bin/tmux-spaces __shim codex /usr/local/bin/harness codex profile default resume conv-id-2"
	if !strings.Contains(joined2, "-t beta:") || !strings.Contains(joined2, "-n codex") || strings.Contains(joined2, "-c /missing/dir") || !strings.Contains(joined2, wantDefaultCmd) || strings.Contains(joined2, "__resume-wrap") {
		t.Errorf("fallback new-window args = %v", newWindows[1])
	}
	if len(switchedTo) != 1 || switchedTo[0] != "beta" {
		t.Errorf("expected switch-client to beta, got %v", switchedTo)
	}

	// Explicit "default" profile keeps window label as the agent name (not "default").
	curSession = "alpha"
	err = b.OpenConversation(context.Background(), model.ConversationEntry{
		Agent:   "codex",
		ID:      "conv-id-3",
		App:     "codex",
		Profile: "default",
		CWD:     "/repo/work",
	}, false)
	if err != nil {
		t.Fatalf("OpenConversation default profile error: %v", err)
	}
	if len(newWindows) != 3 {
		t.Fatalf("expected 3 new-window calls, got %d", len(newWindows))
	}
	joined3 := strings.Join(newWindows[2], " ")
	wantExplicitDefaultCmd := "/usr/local/bin/tmux-spaces __shim --profile default codex /usr/local/bin/harness codex profile default resume conv-id-3"
	if !strings.Contains(joined3, "-n codex") || strings.Contains(joined3, "-n default") || !strings.Contains(joined3, wantExplicitDefaultCmd) || strings.Contains(joined3, "__resume-wrap") {
		t.Errorf("default profile new-window args = %v", newWindows[2])
	}
}
