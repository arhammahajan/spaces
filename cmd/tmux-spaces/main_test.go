package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/cli"
	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/resurrect"
	"github.com/arhammahajan/spaces/internal/tmux"
	"github.com/arhammahajan/spaces/internal/tui"
)

type fakeRunner struct {
	mu      sync.Mutex
	calls   [][]string
	handler func(args []string) (string, string, int, error)
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]string(nil), args...)
	f.calls = append(f.calls, cp)
	if f.handler != nil {
		return f.handler(args)
	}
	return "", "", 0, nil
}

func TestBuildEnvHelpAndStatusBar(t *testing.T) {
	origExec := model.ExecFn
	defer func() { model.ExecFn = origExec }()
	model.ExecFn = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"ok":true,"version":1,"now_ms":0,"panes":{}}`), nil
	}
	freshTs := time.Now().UnixMilli()
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "has-session":
				return "", "", 0, nil
			case "display-message":
				return "work\n", "", 0, nil
			case "list-sessions":
				return "$0\x1fwork\x1f1\x1f1700000000\x1f1\n", "", 0, nil
			case "list-windows":
				return "work\x1f@1\x1f0\x1fclaude\x1f1\x1f0\x1f0\x1f0\n", "", 0, nil
			case "list-panes":
				return fmt.Sprintf("work\x1f@1\x1f0\x1fclaude\x1f%%1\x1fclaude\x1fclaude\x1f/repo\x1f1234\x1f1\x1f1\x1f0\x1f0\x1f0\x1f0\x1fworking\x1fclaude\x1f\x1fediting\x1f%d\n", freshTs), "", 0, nil
			}
			return "", "", 0, nil
		},
	}
	client := tmux.NewClient(runner)
	client.Getenv = func(k string) string {
		if k == "TMUX" {
			return "/tmp/tmux-1000/default,1,0"
		}
		if k == "TMUX_PANE" {
			return "%1"
		}
		return ""
	}

	env := buildEnv(client)
	var out, errBuf bytes.Buffer
	env.Stdout = &out
	env.Stderr = &errBuf

	if code := cli.Run([]string{"status-bar", "--plain"}, env); code != cli.ExitOK {
		t.Fatalf("status-bar exit = %d, stderr = %q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "◐1") {
		t.Fatalf("status-bar output = %q, want working badge", out.String())
	}
}

func TestTUIBackendLoadSpacesAndLaunchAgent(t *testing.T) {
	origExec := model.ExecFn
	defer func() { model.ExecFn = origExec }()
	model.ExecFn = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"ok":true,"version":1,"now_ms":1700000001000,"panes":{"work":{"1":{"agent":"claude","app":"claude-code","state":"blocked","kind":"permission","ts_ms":1700000000500,"text":"approve bash"}}}}`), nil
	}

	var launched [][]string
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "has-session":
				return "", "", 0, nil
			case "display-message":
				return "work\n", "", 0, nil
			case "list-sessions":
				return "$0\x1fwork\x1f1\x1f1700000000\x1f1\n", "", 0, nil
			case "list-windows":
				return "work\x1f@1\x1f0\x1fclaude\x1f1\x1f0\x1f0\x1f0\n", "", 0, nil
			case "list-panes":
				return "work\x1f@1\x1f0\x1fclaude\x1f%1\x1fclaude\x1fclaude\x1f/repo\x1f1234\x1f0\x1f1\x1f0\x1f0\x1f0\x1f0\x1fidle\x1fclaude\x1f\x1f\x1f1700000000000\n", "", 0, nil
			case "capture-pane":
				return "Allow tool? [y/n]\n", "", 0, nil
			case "new-window":
				launched = append(launched, append([]string(nil), args...))
				return "", "", 0, nil
			}
			return "", "", 0, nil
		},
	}
	client := tmux.NewClient(runner)
	b := newTUIBackend(client)
	b.selfPath = "/usr/local/bin/tmux-spaces"
	b.now = func() time.Time { return time.UnixMilli(1700000001000) }

	spaces, err := b.LoadSpaces(context.Background())
	if err != nil {
		t.Fatalf("LoadSpaces error: %v", err)
	}
	if len(spaces) != 1 || len(spaces[0].Panes) != 1 {
		t.Fatalf("unexpected spaces: %+v", spaces)
	}
	if spaces[0].Panes[0].Status != model.StatusNeedsYou {
		t.Fatalf("pane status = %v, want StatusNeedsYou (hook permission override)", spaces[0].Panes[0].Status)
	}

	if err := b.LaunchAgent(context.Background(), "work", "/repo", "codex", ""); err != nil {
		t.Fatalf("LaunchAgent raw error: %v", err)
	}
	if err := b.LaunchAgent(context.Background(), "work", "/repo", "codex", "codex-work"); err != nil {
		t.Fatalf("LaunchAgent profile error: %v", err)
	}
	if len(launched) != 2 {
		t.Fatalf("expected 2 new-window calls, got %d", len(launched))
	}
	if !strings.Contains(strings.Join(launched[0], " "), "__shim codex codex") {
		t.Fatalf("raw launch args = %v", launched[0])
	}
	if !strings.Contains(strings.Join(launched[1], " "), "__shim --profile codex-work codex") ||
		strings.Contains(strings.Join(launched[1], " "), "codex profile codex-work") ||
		strings.Contains(strings.Join(launched[1], " "), "__shim-profile") {
		t.Fatalf("profile launch args = %v", launched[1])
	}
}

func TestParsePopupOptions(t *testing.T) {
	opts := parsePopupOptions([]string{"--new", "--query", "claude", "--target", "work"})
	if !opts.OpenNew || opts.InitialQuery != "claude" || opts.InitialMode != tui.ModeSearch || opts.Target != "work" {
		t.Fatalf("unexpected opts: %+v", opts)
	}
}

func TestCLIThreeLayerStatusAndHarnessResolution(t *testing.T) {
	origExec := model.ExecFn
	defer func() { model.ExecFn = origExec }()
	nowMs := time.Now().UnixMilli()
	// Pane %1 has stale @spaces_state=blocked (2h old) on claude -> expires, screen says idle.
	// Pane %2 has no @spaces_state on agy, but screen says working (esc to cancel).
	// Pane %3 has generic command "node", identified by hook as "codex" working.
	model.ExecFn = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"ok":true,"version":1,"now_ms":%d,"panes":{"work":{"3":{"agent":"codex","app":"codex","state":"working","ts_ms":%d,"text":"running tests"}}}}`, nowMs, nowMs-1000)), nil
	}
	staleTs := nowMs - int64(2*time.Hour/time.Millisecond)
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "has-session":
				return "", "", 0, nil
			case "display-message":
				return "work\x1f%1\n", "", 0, nil
			case "list-sessions":
				return "$0\x1fwork\x1f1\x1f1700000000\x1f1\n", "", 0, nil
			case "list-windows":
				return "work\x1f@1\x1f0\x1fwork\x1f1\x1f0\x1f0\x1f0\n", "", 0, nil
			case "list-panes":
				return strings.Join([]string{
					fmt.Sprintf("work\x1f@1\x1f0\x1fclaude\x1f%%1\x1f✳ Topic\x1fclaude\x1f/repo\x1f0\x1f0\x1f1\x1f0\x1f0\x1f0\x1f0\x1fblocked\x1fclaude-code\x1f\x1fold\x1f%d\x1f0\x1f\x1f\x1fosc7501", staleTs),
					"work\x1f@1\x1f0\x1fagy\x1f%2\x1fagy\x1fagy\x1f/repo\x1f0\x1f0\x1f1\x1f0\x1f0\x1f0\x1f0\x1f\x1f\x1f\x1f\x1f\x1f1\x1f\x1f\x1f",
					"work\x1f@1\x1f0\x1fnode\x1f%3\x1fnode\x1fnode\x1f/repo\x1f0\x1f0\x1f1\x1f0\x1f0\x1f0\x1f0\x1f\x1f\x1f\x1f\x1f\x1f2\x1f\x1f\x1f",
				}, "\n") + "\n", "", 0, nil
			case "capture-pane":
				for i, a := range args {
					if a == "-t" && i+1 < len(args) {
						switch args[i+1] {
						case "%1":
							return "────────────────────\n❯ \n────────────────────\n", "", 0, nil
						case "%2":
							return "───────── esc to cancel\n", "", 0, nil
						}
					}
				}
				return "", "", 0, nil
			}
			return "", "", 0, nil
		},
	}
	client := tmux.NewClient(runner)
	env := buildEnv(client)
	var out, errBuf bytes.Buffer
	env.Stdout = &out
	env.Stderr = &errBuf

	if code := cli.Run([]string{"status-bar", "--plain"}, env); code != cli.ExitOK {
		t.Fatalf("status-bar exit = %d, stderr = %q", code, errBuf.String())
	}
	// %1 expired to idle, %2 screen=working, %3 hook=working -> ◐2 (no stale ●1).
	if got := strings.TrimSpace(out.String()); got != "◐2" {
		t.Fatalf("status-bar = %q, want ◐2", got)
	}

	out.Reset()
	if code := cli.Run([]string{"panes"}, env); code != cli.ExitOK {
		t.Fatalf("panes exit = %d", code)
	}
	panesOut := out.String()
	if !strings.Contains(panesOut, "codex") {
		t.Errorf("panes should resolve hook app codex for %%3:\n%s", panesOut)
	}
	if strings.Contains(panesOut, "needs") {
		t.Errorf("stale blocked pane %%1 should have expired to idle:\n%s", panesOut)
	}
}

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "tmux-spaces-cmd-test-*")
	if err == nil {
		defer os.RemoveAll(tmp)
		model.ConversationHomeDirFn = func() string { return tmp }
		model.ConversationOverlayPathFn = func() string { return filepath.Join(tmp, "conversations-overlay.json") }
		model.ConversationCachePathFn = func() string { return filepath.Join(tmp, "conversations-cache.json") }
		model.ResetConversationStoreCache()
	}
	os.Exit(m.Run())
}

func TestMultipleAntigravityPanesResolveDistinctTitles(t *testing.T) {
	tmpHome := t.TempDir()
	agyDir := filepath.Join(tmpHome, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(agyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agyDir, "conv-alpha.json"), []byte(
		`{"id":"conv-alpha","title":"Alpha Conversation","cwd":"/repo","updated_at":"2026-11-22T10:00:00Z","messages":[{"role":"user","content":"alpha"}]}`,
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agyDir, "conv-beta.json"), []byte(
		`{"id":"conv-beta","title":"Beta Conversation","cwd":"/repo","updated_at":"2026-11-22T11:00:00Z","messages":[{"role":"user","content":"beta"}]}`,
	), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := model.ConversationHomeDirFn
	origOverlay := model.ConversationOverlayPathFn
	origCache := model.ConversationCachePathFn
	origExec := model.ExecFn
	origProcAgy := resurrect.ProcAntigravitySessionFunc
	model.ConversationHomeDirFn = func() string { return tmpHome }
	model.ConversationOverlayPathFn = func() string { return filepath.Join(tmpHome, "overlay.json") }
	model.ConversationCachePathFn = func() string { return filepath.Join(tmpHome, "cache.json") }
	model.ResetConversationStoreCache()
	defer func() {
		model.ConversationHomeDirFn = origHome
		model.ConversationOverlayPathFn = origOverlay
		model.ConversationCachePathFn = origCache
		model.ResetConversationStoreCache()
		model.ExecFn = origExec
		resurrect.ProcAntigravitySessionFunc = origProcAgy
	}()

	resurrect.ProcAntigravitySessionFunc = func(pid int) string {
		switch pid {
		case 101:
			return "conv-alpha"
		case 102:
			return "conv-beta"
		default:
			return ""
		}
	}

	model.ExecFn = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte(`{"ok":true,"version":1,"now_ms":0,"panes":{}}`), nil
	}

	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "has-session":
				return "", "", 0, nil
			case "display-message":
				return "work\n", "", 0, nil
			case "list-sessions":
				return "$0\x1fwork\x1f1\x1f1700000000\x1f1\n", "", 0, nil
			case "list-windows":
				return "work\x1f@1\x1f0\x1fagy\x1f1\x1f0\x1f0\x1f0\n", "", 0, nil
			case "list-panes":
				return strings.Join([]string{
					"work\x1f@1\x1f0\x1fagy\x1f%1\x1fagy\x1fagy\x1f/repo\x1f101\x1f1\x1f1\x1f0\x1f0\x1f0\x1f0\x1f\x1f\x1f\x1f\x1f\x1f0\x1f\x1f\x1f",
					"work\x1f@1\x1f0\x1fagy\x1f%2\x1fagy\x1fagy\x1f/repo\x1f102\x1f0\x1f1\x1f0\x1f0\x1f0\x1f0\x1f\x1f\x1f\x1f\x1f\x1f1\x1f\x1f\x1f",
					"work\x1f@1\x1f0\x1fagy\x1f%3\x1fagy\x1fagy\x1f/repo\x1f103\x1f0\x1f1\x1f0\x1f0\x1f0\x1f0\x1f\x1f\x1f\x1f\x1f\x1f2\x1f\x1f\x1f",
				}, "\n") + "\n", "", 0, nil
			case "capture-pane":
				return "", "", 0, nil
			}
			return "", "", 0, nil
		},
	}

	client := tmux.NewClient(runner)
	b := newTUIBackend(client)
	now := time.UnixMilli(1700000001000)
	b.now = func() time.Time { return now }

	spaces, err := b.LoadSpaces(context.Background())
	if err != nil {
		t.Fatalf("LoadSpaces error: %v", err)
	}
	if len(spaces) != 1 || len(spaces[0].Panes) != 3 {
		t.Fatalf("unexpected spaces: %+v", spaces)
	}
	if got := spaces[0].Panes[0].ConversationTitle; got != "Alpha Conversation" {
		t.Errorf("pane %%1 ConversationTitle = %q, want Alpha Conversation", got)
	}
	if got := spaces[0].Panes[1].ConversationTitle; got != "Beta Conversation" {
		t.Errorf("pane %%2 ConversationTitle = %q, want Beta Conversation", got)
	}
	if got := spaces[0].Panes[2].ConversationTitle; got != "" {
		t.Errorf("pane %%3 ConversationTitle = %q, want empty (no shared fallback when multiple agy panes in same CWD)", got)
	}

	// Switching conversation in pane %1 within screenCaptureInterval must invalidate cache.
	resurrect.ProcAntigravitySessionFunc = func(pid int) string {
		if pid == 101 {
			return "conv-beta"
		}
		if pid == 102 {
			return "conv-alpha"
		}
		return ""
	}
	now = now.Add(500 * time.Millisecond)
	spaces, err = b.LoadSpaces(context.Background())
	if err != nil {
		t.Fatalf("LoadSpaces second call error: %v", err)
	}
	if got := spaces[0].Panes[0].ConversationTitle; got != "Beta Conversation" {
		t.Errorf("pane %%1 updated ConversationTitle = %q, want Beta Conversation", got)
	}
	if got := spaces[0].Panes[1].ConversationTitle; got != "Alpha Conversation" {
		t.Errorf("pane %%2 updated ConversationTitle = %q, want Alpha Conversation", got)
	}
}

func TestPopulateSessionStoreTitlesSupersedesPromptAndRefreshesOutcomes(t *testing.T) {
	tmpHome := t.TempDir()
	agyDir := filepath.Join(tmpHome, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(agyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agyDir, "conv-live.json"), []byte(
		`{"id":"conv-live","title":"Proper Generated Title","cwd":"/repo","updated_at":"2026-11-23T04:00:00Z","messages":[{"role":"user","content":"raw prompt text"}]}`,
	), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := model.ConversationHomeDirFn
	origOverlay := model.ConversationOverlayPathFn
	origCache := model.ConversationCachePathFn
	model.ConversationHomeDirFn = func() string { return tmpHome }
	model.ConversationOverlayPathFn = func() string { return filepath.Join(tmpHome, "overlay.json") }
	model.ConversationCachePathFn = func() string { return filepath.Join(tmpHome, "cache.json") }
	model.ResetConversationStoreCache()
	defer func() {
		model.ConversationHomeDirFn = origHome
		model.ConversationOverlayPathFn = origOverlay
		model.ConversationCachePathFn = origCache
		model.ResetConversationStoreCache()
	}()

	b := newTUIBackend(nil)
	now := time.UnixMilli(1700000001000)
	spaces := []model.Space{
		{
			Name: "work",
			Panes: []model.Pane{
				{
					ID:                "%1",
					App:               "antigravity",
					CWD:               "/repo",
					ConversationTitle: "raw prompt text",
					Hook: &model.HookSummary{
						SessionID: "conv-live",
						Title:     "raw prompt text",
					},
				},
			},
		},
	}
	b.lastOutcomes = []model.OutcomeEntry{
		{
			Session:   "work",
			Pane:      "1",
			Agent:     "antigravity",
			App:       "antigravity",
			Outcome:   "done",
			Title:     "raw prompt text",
			SessionID: "conv-live",
			CWD:       "/repo",
		},
	}
	b.hasOutcomes = true

	b.populateSessionStoreTitles(context.Background(), spaces, now)
	b.enrichOutcomeTitles(context.Background())

	if got := spaces[0].Panes[0].ConversationTitle; got != "Proper Generated Title" {
		t.Errorf("pane %%1 ConversationTitle = %q, want Proper Generated Title", got)
	}
	if got := b.lastOutcomes[0].Title; got != "Proper Generated Title" {
		t.Errorf("outcome[0].Title = %q, want Proper Generated Title", got)
	}
}

func TestTUIBackendLoadOutcomesAndOpenOutcomeInNewTab(t *testing.T) {
	origExec := model.ExecFn
	defer func() { model.ExecFn = origExec }()

	nowMs := int64(1700000005000)
	var removedPayloads []string
	model.ExecFn = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "__space-hooks" && args[1] == "--remove-outcome" {
			removedPayloads = append(removedPayloads, args[2])
			return []byte(`{"ok":true}`), nil
		}
		return []byte(fmt.Sprintf(`{"ok":true,"version":1,"now_ms":%d,"panes":{},"outcomes":[{"session":"beta","pane":"7","agent":"claude","app":"claude-code","profile":"work","outcome":"done","title":"Fix auth","text":"done","session_id":"sess-42","cwd":"/repo/beta","ts_ms":%d}]}`, nowMs, nowMs-60000)), nil
	}

	var newWindows [][]string
	var switchedTo []string
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "list-sessions":
				return "$0\x1falpha\x1f1\x1f1700000000\x1f1\n$1\x1fbeta\x1f0\x1f1700000000\x1f1\n", "", 0, nil
			case "display-message":
				return "alpha\n", "", 0, nil
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
	b.dirExists = func(p string) bool { return p == "/repo/beta" }
	b.profileExists = func(agentName, profile string) bool { return true }
	b.lookPath = func(bin string) (string, error) {
		if bin == "harness" {
			return "/usr/local/bin/harness", nil
		}
		return "", fmt.Errorf("not found: %s", bin)
	}

	outcomes, err := b.LoadOutcomes(context.Background())
	if err != nil {
		t.Fatalf("LoadOutcomes error: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Session != "beta" || outcomes[0].SessionID != "sess-42" || outcomes[0].Profile != "work" {
		t.Fatalf("unexpected outcomes: %+v", outcomes)
	}

	// Open outcome whose space ("beta") is running -> opens new window in beta
	// running `tmux-spaces __shim --profile work claude-code /usr/local/bin/tmux-spaces claude profile work --resume sess-42`
	// and switches client to beta.
	if err := b.OpenOutcome(context.Background(), outcomes[0]); err != nil {
		t.Fatalf("OpenOutcome error: %v", err)
	}
	if len(newWindows) != 1 {
		t.Fatalf("expected 1 new-window call, got %d", len(newWindows))
	}
	joined := strings.Join(newWindows[0], " ")
	wantCmd := "/usr/local/bin/tmux-spaces __shim --profile work claude-code /usr/local/bin/tmux-spaces __resume-access '' claude profile work --resume sess-42"
	for _, want := range []string{"-t beta:", "-c /repo/beta", "-n work", "/bin/sh -c", wantCmd, "status clear", `exec "${SHELL:-/bin/sh}"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("new-window args missing %q: %v", want, newWindows[0])
		}
	}
	if strings.Contains(joined, "__resume-wrap") {
		t.Errorf("new-window args should not use __resume-wrap: %v", newWindows[0])
	}
	if len(switchedTo) != 1 || switchedTo[0] != "beta" {
		t.Errorf("switchedTo = %v, want [beta]", switchedTo)
	}
	if len(removedPayloads) != 1 || !strings.Contains(removedPayloads[0], `"sess-42"`) {
		t.Errorf("expected --remove-outcome for sess-42 on reopen, got %v", removedPayloads)
	}
	if after, err := b.LoadOutcomes(context.Background()); err != nil || len(after) != 0 {
		t.Errorf("expected reopened outcome removed from cache, got %+v (err=%v)", after, err)
	}

	// Open outcome whose originating space ("gone") is closed, profile is empty, and CWD no longer exists ->
	// falls back to current session ("alpha") without -c, uses profile "default", without switching away from alpha.
	err = b.OpenOutcome(context.Background(), model.OutcomeEntry{
		Session:   "gone",
		Agent:     "codex",
		App:       "codex",
		Outcome:   "failed",
		SessionID: "sess-99",
		CWD:       "/deleted/dir",
	})
	if err != nil {
		t.Fatalf("OpenOutcome fallback error: %v", err)
	}
	if len(newWindows) != 2 {
		t.Fatalf("expected 2 new-window calls, got %d", len(newWindows))
	}
	joined2 := strings.Join(newWindows[1], " ")
	wantCmdDefault := "/usr/local/bin/tmux-spaces __shim --profile default codex /usr/local/bin/tmux-spaces __resume-access '' codex profile default resume sess-99"
	if !strings.Contains(joined2, "-t alpha:") || !strings.Contains(joined2, "-n codex") || !strings.Contains(joined2, wantCmdDefault) || !strings.Contains(joined2, "SPACES_PRIOR_OUTCOME=failed") || strings.Contains(joined2, "__resume-wrap") {
		t.Errorf("fallback new-window args = %v", newWindows[1])
	}
	if strings.Contains(joined, "SPACES_PRIOR_OUTCOME=failed") {
		t.Errorf("done outcome should not carry SPACES_PRIOR_OUTCOME=failed: %v", newWindows[0])
	}
	if strings.Contains(joined2, "-c /deleted/dir") {
		t.Errorf("deleted CWD should not be passed to new-window: %v", newWindows[1])
	}
	if len(switchedTo) != 1 {
		t.Errorf("should not switch-client when already in alpha, switchedTo = %v", switchedTo)
	}
}

func TestScreenWorkingToDonePersistsAcrossShortLivedProcessesForTenMinutes(t *testing.T) {
	origExec := model.ExecFn
	defer func() { model.ExecFn = origExec }()
	model.ExecFn = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "__space-hooks" && args[1] == "--record-outcome" {
			return []byte(`{"ok":true}`), nil
		}
		return []byte(`{"ok":true,"version":1,"now_ms":0,"panes":{}}`), nil
	}

	// Simulate tmux per-pane options persisting across separate tmux-spaces invocations.
	paneState := "idle"
	paneTs := "1700000000000"
	paneSource := "osc7501"
	paneViewport := "───────── esc to cancel\n"

	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "has-session":
				return "", "", 0, nil
			case "display-message":
				return "work\x1f%25\n", "", 0, nil
			case "list-sessions":
				return "$0\x1fwork\x1f1\x1f1700000000\x1f1\n", "", 0, nil
			case "list-windows":
				return "work\x1f@15\x1f0\x1fpratham\x1f1\x1f0\x1f0\x1f0\n", "", 0, nil
			case "list-panes":
				return fmt.Sprintf("work\x1f@15\x1f0\x1fpratham\x1f%%25\x1fagy\x1fharness\x1f/repo\x1f0\x1f1\x1f1\x1f0\x1f0\x1f0\x1f0\x1f%s\x1fantigravity\x1f\x1f\x1f%s\x1f0\x1fsess-1\x1fpratham\x1f%s\x1f\n",
					paneState, paneTs, paneSource), "", 0, nil
			case "capture-pane":
				return paneViewport, "", 0, nil
			case "set-option":
				if len(args) >= 6 {
					switch args[4] {
					case "@spaces_state":
						paneState = args[5]
					case "@spaces_ts":
						paneTs = args[5]
					case "@spaces_source":
						paneSource = args[5]
					}
				}
				return "", "", 0, nil
			}
			return "", "", 0, nil
		},
	}

	// Invocation 1 (e.g. status-bar while agent is working): persists @spaces_state=working.
	b1 := newTUIBackend(tmux.NewClient(runner))
	b1.now = func() time.Time { return time.UnixMilli(1700000010000) }
	spaces, err := b1.LoadSpaces(context.Background())
	if err != nil || spaces[0].Panes[0].Status != model.StatusWorking {
		t.Fatalf("invocation 1: status=%v err=%v", spaces[0].Panes[0].Status, err)
	}
	if paneState != "working" || paneSource != "screen" {
		t.Fatalf("expected working state persisted to tmux, got state=%q source=%q", paneState, paneSource)
	}

	// Invocation 2 (brand-new process after turn finishes -> idle prompt): sees working->done
	// even though pane is active (active=1) and persists @spaces_state=done.
	paneViewport = "Type your message\n"
	b2 := newTUIBackend(tmux.NewClient(runner))
	b2.now = func() time.Time { return time.UnixMilli(1700000020000) }
	spaces, err = b2.LoadSpaces(context.Background())
	if err != nil || spaces[0].Panes[0].Status != model.StatusDone {
		t.Fatalf("invocation 2: status=%v err=%v, want StatusDone", spaces[0].Panes[0].Status, err)
	}
	if paneState != "done" || paneSource != "screen" {
		t.Fatalf("expected done state persisted to tmux, got state=%q source=%q", paneState, paneSource)
	}

	// Invocation 3 (fresh Inbox popup 5 minutes later): still StatusDone.
	b3 := newTUIBackend(tmux.NewClient(runner))
	b3.now = func() time.Time { return time.UnixMilli(1700000020000 + 5*60_000) }
	spaces, err = b3.LoadSpaces(context.Background())
	if err != nil || spaces[0].Panes[0].Status != model.StatusDone {
		t.Fatalf("invocation 3 (+5m): status=%v err=%v, want StatusDone", spaces[0].Panes[0].Status, err)
	}

	// Invocation 4 (fresh Inbox popup 10m+1s later): decays cleanly to StatusIdle.
	b4 := newTUIBackend(tmux.NewClient(runner))
	b4.now = func() time.Time { return time.UnixMilli(1700000020000 + 10*60_000 + 1000) }
	spaces, err = b4.LoadSpaces(context.Background())
	if err != nil || spaces[0].Panes[0].Status != model.StatusIdle {
		t.Fatalf("invocation 4 (+10m1s): status=%v err=%v, want StatusIdle", spaces[0].Panes[0].Status, err)
	}
}

func TestCtrlDExitedAntigravityClearsStaleOptionsAndShowsOutcomeInInbox(t *testing.T) {
	origExec := model.ExecFn
	origDetect := model.DetectDescendantAgentFn
	origLive := model.ProcIsLiveFn
	defer func() {
		model.ExecFn = origExec
		model.DetectDescendantAgentFn = origDetect
		model.ProcIsLiveFn = origLive
	}()
	model.DetectDescendantAgentFn = func(int) string { return "" }
	model.ProcIsLiveFn = func(pid int) bool { return pid == 438039 }

	nowMs := int64(1700000050000)
	model.ExecFn = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{
			"ok": true,
			"version": 1,
			"now_ms": %d,
			"panes": {
				"model-router": {
					"41": {
						"agent": "antigravity",
						"app": "antigravity",
						"state": "done",
						"event": "stop",
						"ts_ms": %d,
						"state_since_ms": %d,
						"text": "turn complete",
						"title": "Rewrite Readme",
						"session_id": "conv-41"
					}
				}
			},
			"outcomes": [
				{
					"session": "model-router",
					"pane": "41",
					"agent": "antigravity",
					"app": "antigravity",
					"profile": "pratham",
					"outcome": "done",
					"text": "finished",
					"title": "Rewrite Readme",
					"session_id": "conv-41",
					"cwd": "/repo",
					"ts_ms": %d
				}
			]
		}`, nowMs, nowMs-5000, nowMs-5000, nowMs-1000)), nil
	}

	unsetOpts := map[string]bool{}
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "has-session":
				return "", "", 0, nil
			case "display-message":
				return "model-router\x1f%41\n", "", 0, nil
			case "list-sessions":
				return "$0\x1fmodel-router\x1f1\x1f1700000000\x1f1\n", "", 0, nil
			case "list-windows":
				return "model-router\x1f@1\x1f0\x1fzsh\x1f1\x1f0\x1f0\x1f0\n", "", 0, nil
			case "list-panes":
				return fmt.Sprintf("model-router\x1f@1\x1f0\x1fzsh\x1f%%41\x1fzsh\x1fzsh\x1f/repo\x1f438039\x1f1\x1f1\x1f0\x1f0\x1f0\x1f0\x1fdone\x1fantigravity\x1f\x1ffinished\x1f%d\x1f0\x1fconv-41\x1f\x1fosc7501\x1fRewrite Readme\n", nowMs-1000), "", 0, nil
			case "set-option":
				if len(args) >= 6 && args[2] == "-t" && args[3] == "%41" && args[4] == "-u" {
					unsetOpts[args[5]] = true
				}
				return "", "", 0, nil
			}
			return "", "", 0, nil
		},
	}

	b := newTUIBackend(tmux.NewClient(runner))
	b.now = func() time.Time { return time.UnixMilli(nowMs) }

	spaces, err := b.LoadSpaces(context.Background())
	if err != nil {
		t.Fatalf("LoadSpaces error: %v", err)
	}
	if len(spaces) != 1 || len(spaces[0].Panes) != 1 {
		t.Fatalf("unexpected spaces: %+v", spaces)
	}
	p := spaces[0].Panes[0]
	if p.App != "shell" || p.IsAgent() {
		t.Fatalf("exited antigravity pane should resolve as non-agent shell, got app=%q isAgent=%v status=%v", p.App, p.IsAgent(), p.Status)
	}
	for _, opt := range []string{"@spaces_app", "@spaces_state", "@spaces_resume_session", "@spaces_title"} {
		if !unsetOpts[opt] {
			t.Errorf("expected stale option %s to be unset on returned shell pane, unset=%v", opt, unsetOpts)
		}
	}
	outcomes, err := b.LoadOutcomes(context.Background())
	if err != nil || len(outcomes) != 1 || outcomes[0].SessionID != "conv-41" {
		t.Fatalf("outcomes = %+v (err=%v), want conv-41 done outcome", outcomes, err)
	}
}

func TestSaveSpacesToStoreSkipsWhenAutoRestorePendingAndPersistsOpenState(t *testing.T) {
	origExec := model.ExecFn
	defer func() { model.ExecFn = origExec }()
	model.ExecFn = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"ok":true,"version":1,"now_ms":0,"panes":{}}`), nil
	}

	restoredOpt := "pending"
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "show-option":
				return restoredOpt + "\n", "", 0, nil
			case "has-session":
				return "", "", 0, nil
			case "display-message":
				return "alpha\x1f%1\n", "", 0, nil
			case "list-sessions":
				return "$0\x1falpha\x1f1\x1f1700000000\x1f1\n", "", 0, nil
			case "list-windows":
				return "alpha\x1f@1\x1f0\x1fzsh\x1f1\x1f0\x1f0\x1f0\n", "", 0, nil
			case "list-panes":
				return "alpha\x1f@1\x1f0\x1fzsh\x1f%1\x1fzsh\x1fzsh\x1f/repo\x1f1234\x1f1\x1f1\x1f0\x1f0\x1f0\x1f0\x1fidle\x1f\x1f\x1f\x1f0\n", "", 0, nil
			}
			return "", "", 0, nil
		},
	}

	store := &resurrect.Store{Dir: t.TempDir()}
	// Pre-populate OpenState as it existed before reboot.
	if err := store.SaveOpenState(resurrect.OpenState{
		ActiveSession: "beta",
		Spaces:        []string{"beta", "gamma"},
	}); err != nil {
		t.Fatalf("SaveOpenState: %v", err)
	}

	client := tmux.NewClient(runner)
	// While @spaces_restored is "pending", saveSpacesToStore must NOT overwrite OpenState.
	n, err := saveSpacesToStore(context.Background(), client, store, "")
	if err != nil || n != 0 {
		t.Fatalf("pending saveSpacesToStore = (%d, %v), want (0, nil)", n, err)
	}
	st, err := store.LoadOpenState()
	if err != nil || len(st.Spaces) != 2 || st.Spaces[0] != "beta" {
		t.Fatalf("OpenState overwritten while pending: %+v, err=%v", st, err)
	}

	// Once @spaces_restored is "done", saveSpacesToStore saves snapshots and updates OpenState.
	restoredOpt = "done"
	n, err = saveSpacesToStore(context.Background(), client, store, "")
	if err != nil || n != 1 {
		t.Fatalf("done saveSpacesToStore = (%d, %v), want (1, nil)", n, err)
	}
	st, err = store.LoadOpenState()
	if err != nil || len(st.Spaces) != 1 || st.Spaces[0] != "alpha" || st.ActiveSession != "alpha" {
		t.Fatalf("OpenState after save = %+v, err=%v", st, err)
	}
}
