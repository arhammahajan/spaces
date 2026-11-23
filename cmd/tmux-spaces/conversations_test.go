package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/tmux"
	"github.com/arhammahajan/spaces/internal/tui"
	_ "modernc.org/sqlite"
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
	// The shim launches the saved profile in resume mode.
	cmd := buildConversationOpenCommand("claude", "work", "sess-123", "/repo", "Fix retries", nil, "/usr/local/bin/tmux-spaces")
	joined := strings.Join(cmd, " ")
	for _, want := range []string{
		"/bin/sh -c",
		"/usr/local/bin/tmux-spaces __shim --profile work claude-code /usr/local/bin/tmux-spaces __resume-access '' claude profile work --resume sess-123",
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

	// Harness-managed agent with empty profile defaults to --profile default.
	cmdDefault := buildConversationOpenCommand("claude", "", "sess-456", "/repo", "Fix retries", nil, "/usr/local/bin/tmux-spaces")
	joinedDefault := strings.Join(cmdDefault, " ")
	if !strings.Contains(joinedDefault, "/usr/local/bin/tmux-spaces __shim --profile default claude-code /usr/local/bin/tmux-spaces __resume-access '' claude profile default --resume sess-456") {
		t.Errorf("default claude cmd = %s", joinedDefault)
	}

	// Non-harness agent (omp) launches directly under __shim without __resume-wrap.
	cmdOmp := buildConversationOpenCommand("omp", "", "omp-sess-7", "/repo", "Omp chat", nil, "/usr/local/bin/tmux-spaces")
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
	cmdCrush := buildConversationOpenCommand("crush", "", "crush-9", "/repo", "Crush chat", nil, "/usr/local/bin/tmux-spaces")
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

func TestBuildConversationOpenCommandReplayLaunchCmd(t *testing.T) {
	// Replays stored LaunchCmd, strips old resume flags,
	// extracts profile from LaunchCmd when profile is empty, and appends the new session ID.
	stored := []string{"/opt/bin/harness", "claude", "profile", "work", "--model", "opus", "--resume", "old-sess"}
	cmd := buildConversationOpenCommand("claude", "", "new-sess-99", "/repo", "Title", stored, "/usr/local/bin/tmux-spaces")
	joined := strings.Join(cmd, " ")
	want := "/usr/local/bin/tmux-spaces __shim --profile work claude-code /usr/local/bin/tmux-spaces __resume-access '' claude profile work --model opus --resume new-sess-99"
	if !strings.Contains(joined, want) {
		t.Errorf("replay launch cmd = %s, want substring %q", joined, want)
	}
	if strings.Contains(joined, "old-sess") {
		t.Errorf("replay launch cmd should strip old session id: %s", joined)
	}

	// Codex subcommand resume flag stripping on replay.
	storedCodex := []string{"/opt/bin/harness", "codex", "profile", "dev", "resume", "old-codex"}
	cmdCodex := buildConversationOpenCommand("codex", "dev", "new-codex", "/repo", "Title", storedCodex, "/usr/local/bin/tmux-spaces")
	joinedCodex := strings.Join(cmdCodex, " ")
	wantCodex := "/usr/local/bin/tmux-spaces __shim --profile dev codex /usr/local/bin/tmux-spaces __resume-access '' codex profile dev resume new-codex"
	if !strings.Contains(joinedCodex, wantCodex) {
		t.Errorf("replay codex launch cmd = %s, want substring %q", joinedCodex, wantCodex)
	}
	if strings.Contains(joinedCodex, "old-codex") {
		t.Errorf("replay codex launch cmd should strip old session id: %s", joinedCodex)
	}
}

func TestTUIBackendOpenConversationTargetsCurrentSessionAndRecords(t *testing.T) {
	tmpHome := t.TempDir()
	overlayPath := filepath.Join(tmpHome, "conversations-overlay.json")
	origHome := model.ConversationHomeDirFn
	origOverlay := model.ConversationOverlayPathFn
	origCache := model.ConversationCachePathFn
	model.ConversationHomeDirFn = func() string { return tmpHome }
	model.ConversationOverlayPathFn = func() string { return overlayPath }
	model.ConversationCachePathFn = func() string { return filepath.Join(tmpHome, "conversations-cache.json") }
	model.ResetConversationStoreCache()
	defer func() {
		model.ConversationHomeDirFn = origHome
		model.ConversationOverlayPathFn = origOverlay
		model.ConversationCachePathFn = origCache
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
	b.profileExists = func(agentName, profile string) bool { return true }
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
	overlayDBPath := filepath.Join(tmpHome, "conversations-overlay.db")
	db, err := sql.Open("sqlite", overlayDBPath)
	if err != nil {
		t.Fatalf("open sqlite overlay db at %s: %v", overlayDBPath, err)
	}
	defer db.Close()
	var gotSID, gotSpace, gotProfile, gotLaunchJSON string
	if err := db.QueryRow(
		`SELECT session_id, space, profile, launch_cmd_json FROM conversation_overlay WHERE agent = ? AND session_id = ?`,
		"claude", "conv-id-1",
	).Scan(&gotSID, &gotSpace, &gotProfile, &gotLaunchJSON); err != nil {
		t.Fatalf("query overlay row from %s: %v", overlayDBPath, err)
	}
	if gotSID != "conv-id-1" || gotSpace != "alpha" || gotProfile != "work" {
		t.Errorf("unexpected recorded overlay row: sid=%q space=%q profile=%q", gotSID, gotSpace, gotProfile)
	}
	if gotLaunchJSON != "" {
		t.Errorf("open-only overlay wrote launch metadata: %q", gotLaunchJSON)
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
	wantDefaultCmd := "/usr/local/bin/tmux-spaces __shim --profile default codex /usr/local/bin/tmux-spaces __resume-access '' codex profile default resume conv-id-2"
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
	wantExplicitDefaultCmd := "/usr/local/bin/tmux-spaces __shim --profile default codex /usr/local/bin/tmux-spaces __resume-access '' codex profile default resume conv-id-3"
	if !strings.Contains(joined3, "-n codex") || strings.Contains(joined3, "-n default") || !strings.Contains(joined3, wantExplicitDefaultCmd) || strings.Contains(joined3, "__resume-wrap") {
		t.Errorf("default profile new-window args = %v", newWindows[2])
	}
}

func TestTUIBackendOpenConversationKeepsLaunchMetadataOwnedByLauncher(t *testing.T) {
	var newWindows [][]string
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "display-message":
				return "alpha\n", "", 0, nil
			case "new-window":
				newWindows = append(newWindows, append([]string(nil), args...))
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
	b.profileExists = func(agentName, profile string) bool { return true }
	b.lookPath = func(bin string) (string, error) {
		if bin == "harness" {
			return "/usr/local/bin/harness", nil
		}
		return "", fmt.Errorf("not found")
	}

	var recorded []model.ConversationEntry
	b.recordConvOpen = func(_ context.Context, conv model.ConversationEntry, _ string, _ int64) error {
		recorded = append(recorded, conv)
		return nil
	}

	// An older conversation uses the fallback launcher without recording a choice.
	err := b.OpenConversation(context.Background(), model.ConversationEntry{
		Agent:   "claude",
		ID:      "conv-1",
		Profile: "work",
		CWD:     "/repo/work",
	}, false)
	if err != nil {
		t.Fatalf("OpenConversation error: %v", err)
	}
	if len(recorded) != 1 || len(recorded[0].LaunchCmd) != 0 {
		t.Fatalf("open event overwrote launch metadata: %v", recorded)
	}

	// An empty profile still uses the default profile at launch.
	err = b.OpenConversation(context.Background(), model.ConversationEntry{
		Agent: "codex",
		ID:    "conv-2",
		CWD:   "/repo/work",
	}, false)
	if err != nil {
		t.Fatalf("OpenConversation error: %v", err)
	}
	if len(recorded) != 2 || len(recorded[1].LaunchCmd) != 0 {
		t.Fatalf("open event overwrote launch metadata: %v", recorded)
	}

	// Existing LaunchCmd is preserved and replayed.
	existing := []string{"/old/tmux-spaces", "__resume-access", "read-only", "claude", "profile", "custom", "--model", "opus"}
	err = b.OpenConversation(context.Background(), model.ConversationEntry{
		Agent:     "claude",
		ID:        "conv-3",
		Profile:   "custom",
		CWD:       "/repo/work",
		LaunchCmd: existing,
	}, false)
	if err != nil {
		t.Fatalf("OpenConversation error: %v", err)
	}
	if len(recorded) != 3 || len(recorded[2].LaunchCmd) != 0 {
		t.Fatalf("open event overwrote launch metadata: %v", recorded)
	}
	if len(newWindows) != 3 || !strings.Contains(strings.Join(newWindows[2], " "), "/usr/local/bin/tmux-spaces __resume-access read-only claude profile custom --model opus --resume conv-3") {
		t.Errorf("replayed new-window args = %v", newWindows[2])
	}

	// Explicit Profile overrides stale LaunchCmd with a different profile.
	stale := []string{"/opt/bin/harness", "claude", "profile", "oldprof", "--model", "opus"}
	err = b.OpenConversation(context.Background(), model.ConversationEntry{
		Agent:     "claude",
		ID:        "conv-4",
		Profile:   "pratham",
		CWD:       "/repo/work",
		LaunchCmd: stale,
	}, false)
	if err != nil {
		t.Fatalf("OpenConversation override error: %v", err)
	}
	if len(recorded) != 4 || len(recorded[3].LaunchCmd) != 0 {
		t.Fatalf("open event overwrote launch metadata: %v", recorded)
	}
	joined4 := strings.Join(newWindows[3], " ")
	wantCmd4 := "/usr/local/bin/tmux-spaces __shim --profile pratham claude-code /usr/local/bin/tmux-spaces __resume-access '' claude profile pratham --resume conv-4"
	if !strings.Contains(joined4, wantCmd4) || strings.Contains(joined4, "oldprof") {
		t.Errorf("override new-window args = %v, want %q without oldprof", newWindows[3], wantCmd4)
	}
}

func TestBuildConversationOpenCommandExplicitProfileOverridesStaleLaunchCmd(t *testing.T) {
	stale := []string{"/opt/bin/harness", "claude", "profile", "work", "--model", "opus", "--resume", "old-sess"}
	cmd := buildConversationOpenCommand("claude", "pratham", "new-sess-1", "/repo", "Title", stale, "/usr/local/bin/tmux-spaces")
	joined := strings.Join(cmd, " ")
	want := "/usr/local/bin/tmux-spaces __shim --profile pratham claude-code /usr/local/bin/tmux-spaces __resume-access '' claude profile pratham --resume new-sess-1"
	if !strings.Contains(joined, want) {
		t.Errorf("override cmd = %s, want substring %q", joined, want)
	}
	if strings.Contains(joined, "work") || strings.Contains(joined, "old-sess") {
		t.Errorf("override cmd must ignore stale launchCmd profile/args: %s", joined)
	}
}

func TestPopulateSessionStoreTitlesRecordsLiveProfileAndDeduplicates(t *testing.T) {
	b := newTUIBackend(nil)
	type launchCall struct {
		agent, sid, profile, cwd, title, space string
		cmd                                    []string
		nowMs                                  int64
	}
	var calls []launchCall
	b.recordConvLaunch = func(_ context.Context, agent, sessionID, profile, cwd, title string, launchCmd []string, space string, nowMs int64) error {
		calls = append(calls, launchCall{
			agent: agent, sid: sessionID, profile: profile, cwd: cwd, title: title, space: space,
			cmd: append([]string(nil), launchCmd...), nowMs: nowMs,
		})
		return nil
	}

	now := time.UnixMilli(1700000999000)
	spaces := []model.Space{
		{
			Name: "alpha",
			Panes: []model.Pane{
				{
					ID:                "%10",
					App:               "antigravity",
					Profile:           "pratham",
					ResumeSession:     "sid-live-1",
					CWD:               "/repo/work",
					ConversationTitle: "Existing Title",
				},
				{
					ID:                "%11",
					App:               "claude-code",
					Profile:           "hook-prof",
					CWD:               "/repo/work",
					ConversationTitle: "Hook Pane",
					Hook: &model.HookSummary{
						SessionID: "sid-hook-2",
					},
				},
			},
		},
	}

	// First call records both live agent panes.
	b.populateSessionStoreTitles(context.Background(), spaces, now)
	if len(calls) != 2 {
		t.Fatalf("expected 2 launch calls, got %d: %+v", len(calls), calls)
	}
	if calls[0].agent != "antigravity" || calls[0].sid != "sid-live-1" || calls[0].profile != "pratham" ||
		calls[0].cwd != "/repo/work" || calls[0].title != "Existing Title" || calls[0].space != "alpha" ||
		calls[0].nowMs != now.UnixMilli() || len(calls[0].cmd) != 0 {
		t.Errorf("calls[0] = %+v", calls[0])
	}
	if calls[1].agent != "claude-code" || calls[1].sid != "sid-hook-2" || calls[1].profile != "hook-prof" ||
		calls[1].space != "alpha" {
		t.Errorf("calls[1] = %+v", calls[1])
	}

	// Second call with identical (paneID, sid, prof) must deduplicate and not upsert again.
	b.populateSessionStoreTitles(context.Background(), spaces, now.Add(time.Second))
	if len(calls) != 2 {
		t.Fatalf("expected deduplication to keep 2 calls, got %d: %+v", len(calls), calls)
	}

	// Changing session ID on pane %10 records once more for the new session.
	spaces[0].Panes[0].ResumeSession = "sid-live-3"
	b.populateSessionStoreTitles(context.Background(), spaces, now.Add(2*time.Second))
	if len(calls) != 3 || calls[2].sid != "sid-live-3" || calls[2].profile != "pratham" {
		t.Fatalf("expected 3rd call for new sid, got %+v", calls)
	}
}

func TestReopenLiveCodexFocusesExistingPane(t *testing.T) {
	var selected, launched int
	runner := &fakeRunner{handler: func(args []string) (string, string, int, error) {
		switch args[0] {
		case "list-panes":
			fields := make([]string, 23)
			fields[0], fields[1], fields[4], fields[6], fields[21] = "work", "@4", "%7", "codex", "live-thread"
			return strings.Join(fields, "\x1f") + "\n", "", 0, nil
		case "display-message":
			return "work\n", "", 0, nil
		case "select-pane":
			if args[len(args)-1] == "%7" {
				selected++
			}
		case "new-window":
			launched++
		}
		return "", "", 0, nil
	}}
	backend := newTUIBackend(tmux.NewClient(runner))
	backend.recordConvOpen = func(context.Context, model.ConversationEntry, string, int64) error { return nil }
	ctx := context.Background()
	conv := model.ConversationEntry{Agent: "codex", ID: "live-thread"}
	if err := backend.OpenConversation(ctx, conv, false); err != nil {
		t.Fatal(err)
	}
	if err := backend.OpenOutcome(ctx, model.OutcomeEntry{Agent: "codex", Session: "work", SessionID: conv.ID}); err != nil {
		t.Fatal(err)
	}
	if selected != 2 || launched != 0 {
		t.Fatalf("ordinary reopen selected %d panes and launched %d windows", selected, launched)
	}
	if err := backend.OpenConversation(ctx, conv, true); err != nil {
		t.Fatal(err)
	}
	if selected != 2 || launched != 1 {
		t.Fatalf("explicit new tab selected %d panes and launched %d windows", selected, launched)
	}
}

func TestNonexistentProfileFallsBackToDefault(t *testing.T) {
	var newWindows [][]string
	runner := &fakeRunner{handler: func(args []string) (string, string, int, error) {
		switch args[0] {
		case "list-panes":
			return "", "", 0, nil
		case "list-sessions":
			return "$1\x1fdev\x1f1\x1f1\x1f1700000000\n", "", 0, nil
		case "display-message":
			return "dev\n", "", 0, nil
		case "new-window":
			newWindows = append(newWindows, append([]string(nil), args...))
		}
		return "", "", 0, nil
	}}
	backend := newTUIBackend(tmux.NewClient(runner))
	backend.selfPath = "/usr/local/bin/tmux-spaces"
	backend.recordConvOpen = func(context.Context, model.ConversationEntry, string, int64) error { return nil }
	backend.profileExists = func(agentName, profile string) bool {
		return profile == "" || profile == "default"
	}

	ctx := context.Background()
	if err := backend.OpenConversation(ctx, model.ConversationEntry{
		Agent:     "codex",
		ID:        "sid-1",
		Profile:   "work",
		LaunchCmd: []string{"/usr/local/bin/tmux-spaces", "__resume-access", "", "codex", "profile", "work"},
	}, false); err != nil {
		t.Fatalf("OpenConversation error: %v", err)
	}
	if len(newWindows) != 1 {
		t.Fatalf("expected 1 new-window call, got %d", len(newWindows))
	}
	joined := strings.Join(newWindows[0], " ")
	if strings.Contains(joined, "work") {
		t.Fatalf("must not launch nonexistent profile work: %s", joined)
	}
	if !strings.Contains(joined, "-n codex") || !strings.Contains(joined, "profile default") || !strings.Contains(joined, "resume sid-1") {
		t.Fatalf("expected fallback to default profile with resume sid-1, got: %s", joined)
	}
}
