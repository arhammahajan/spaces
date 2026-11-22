package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSpaceHookTitles(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	outPath, _ := captureOutput(t)

	calls := 0
	DiscoverTitleFunc = func(agent, cwd, sessionID string) string {
		calls++
		if agent == "claude" && sessionID == "s1" {
			return "Refactor hooks"
		}
		return ""
	}
	SpaceHooksSummary = func() map[string]any { return map[string]any{"ok": true} }

	queries := `[{"key":"p1","agent":"claude","cwd":"/repo","session_id":"s1"},{"key":"p2","agent":"claude","cwd":"/repo","session_id":"s1"},{"key":"p3","agent":"codex","cwd":"/repo","session_id":"s2"}]`
	if code := SpaceHooks([]string{"--titles", queries}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	var resp struct {
		OK     bool              `json:"ok"`
		Titles map[string]string `json:"titles"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(readFile(t, outPath))), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Titles["p1"] != "Refactor hooks" || resp.Titles["p2"] != "Refactor hooks" {
		t.Errorf("resp = %+v", resp)
	}
	if _, exists := resp.Titles["p3"]; exists {
		t.Errorf("unexpected p3 in %+v", resp.Titles)
	}
	// Duplicate (agent, cwd, sid) query must be cached.
	if calls != 2 {
		t.Errorf("DiscoverTitleFunc calls = %d, want 2", calls)
	}
}

func TestSpaceHookRecordAndRemoveOutcome(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	clearSpaceEnv(t)
	outPath, _ := captureOutput(t)

	// RecordOutcome enriches from tmux pane options and DiscoverTitleFunc.
	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "mysess\x1f/work/app\x1fprofA\x1fsid-42\x1f\x1ffailed\n", 0, nil)
	RecordConversationLaunchFunc = nil
	DiscoverTitleFunc = func(agent, cwd, sid string) string {
		if agent == "claude" && sid == "sid-42" {
			return "Debug CI"
		}
		return ""
	}
	var recorded map[string]any
	RecordOutcome = func(entry map[string]any) error {
		recorded = entry
		return nil
	}

	// Closing a pane with priorOutcome=failed and text="finished" keeps outcome="failed".
	payload := `{"pane":"%9","app":"claude-code","outcome":"done","text":"finished"}`
	if code := SpaceHookRecordOutcome([]string{payload}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(readFile(t, outPath))), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["ok"] != true {
		t.Fatalf("resp = %v", resp)
	}
	if recorded["session"] != "mysess" || recorded["pane"] != "9" ||
		recorded["cwd"] != "/work/app" || recorded["profile"] != "profA" ||
		recorded["session_id"] != "sid-42" || recorded["agent"] != "claude" ||
		recorded["title"] != "Debug CI" || recorded["outcome"] != "failed" {
		t.Errorf("recorded = %v", recorded)
	}

	// RemoveOutcome dispatches cleanly.
	resetFile(t, Stdout, outPath)
	var removed map[string]any
	RemoveOutcome = func(match map[string]any) error {
		removed = match
		return nil
	}
	if code := SpaceHookRemoveOutcome([]string{`{"session_id":"sid-42"}`}); code != 0 {
		t.Fatalf("remove code = %d", code)
	}
	if removed["session_id"] != "sid-42" {
		t.Errorf("removed = %v", removed)
	}
}

func TestSpaceHookRecordOutcomeCapturesLaunchCmd(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	clearSpaceEnv(t)
	outPath, _ := captureOutput(t)

	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "mysess\x1f/work/app\x1fprofA\x1fsid-99\x1fMy Title\x1f\x1f[\"/usr/local/bin/harness\",\"claude\",\"profile\",\"profA\"]\n", 0, nil)
	RecordOutcome = func(entry map[string]any) error { return nil }

	var gotAgent, gotSID, gotProfile, gotCWD, gotTitle, gotSpace string
	var gotCmd []string
	RecordConversationLaunchFunc = func(_ context.Context, agent, sessionID, profile, cwd, title string, launchCmd []string, space string, _ int64) error {
		gotAgent, gotSID, gotProfile, gotCWD, gotTitle, gotSpace = agent, sessionID, profile, cwd, title, space
		gotCmd = append([]string(nil), launchCmd...)
		return nil
	}

	payload := `{"pane":"%9","app":"claude-code","outcome":"done","text":"finished"}`
	if code := SpaceHookRecordOutcome([]string{payload}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	_ = outPath
	wantCmd := []string{"/usr/local/bin/harness", "claude", "profile", "profA"}
	if gotAgent != "claude" || gotSID != "sid-99" || gotProfile != "profA" ||
		gotCWD != "/work/app" || gotTitle != "My Title" || gotSpace != "mysess" ||
		!reflect.DeepEqual(gotCmd, wantCmd) {
		t.Fatalf("recorded outcome launch = (%q, %q, %q, %q, %q, %q, %v)",
			gotAgent, gotSID, gotProfile, gotCWD, gotTitle, gotSpace, gotCmd)
	}
}

func TestSpaceHookRecordOutcomeRecordsProfileWithoutLaunchCmd(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	clearSpaceEnv(t)
	_, _ = captureOutput(t)

	type rec struct {
		agent, sid, profile, cwd, title, space string
		cmd                                    []string
	}
	var recorded []rec
	RecordConversationLaunchFunc = func(_ context.Context, agent, sessionID, profile, cwd, title string, launchCmd []string, space string, _ int64) error {
		recorded = append(recorded, rec{
			agent: agent, sid: sessionID, profile: profile, cwd: cwd, title: title, space: space,
			cmd: append([]string(nil), launchCmd...),
		})
		return nil
	}
	RecordOutcome = func(entry map[string]any) error { return nil }

	// 1. @spaces_profile set, @spaces_launch_cmd empty -> still records profile.
	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "mysess\x1f/work/app\x1fpratham\x1fsid-101\x1fFix bug\x1f\x1f\n", 0, nil)
	if code := SpaceHookRecordOutcome([]string{`{"pane":"%9","app":"antigravity","outcome":"done","text":"finished"}`}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(recorded) != 1 || recorded[0].agent != "antigravity" || recorded[0].sid != "sid-101" ||
		recorded[0].profile != "pratham" || recorded[0].cwd != "/work/app" ||
		recorded[0].title != "Fix bug" || recorded[0].space != "mysess" || len(recorded[0].cmd) != 0 {
		t.Fatalf("case 1 recorded = %+v", recorded)
	}

	// 2. @spaces_profile empty, @spaces_launch_cmd has profile -> extracts profile into entry and overlay.
	recorded = nil
	var lastEntry map[string]any
	RecordOutcome = func(entry map[string]any) error { lastEntry = entry; return nil }
	TmuxFunc = stubTmux(&calls, "mysess\x1f/work/app\x1f\x1fsid-102\x1fAnother\x1f\x1f[\"/usr/local/bin/harness\",\"claude\",\"profile\",\"extracted\"]\n", 0, nil)
	if code := SpaceHookRecordOutcome([]string{`{"pane":"%9","app":"claude-code","outcome":"done","text":"finished"}`}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if lastEntry["profile"] != "extracted" {
		t.Fatalf("entry profile = %v, want extracted", lastEntry["profile"])
	}
	if len(recorded) != 1 || recorded[0].profile != "extracted" {
		t.Fatalf("case 2 recorded = %+v", recorded)
	}
}

func TestDiscoverProfileAntigravity(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	brainDir := filepath.Join(tmpHome, ".harness", "profiles", "antigravity", "work-prof", "home", ".gemini", "antigravity-cli", "brain", "conv-123")
	if err := os.MkdirAll(brainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverProfile("agy", "/repo", "conv-123"); got != "work-prof" {
		t.Errorf("DiscoverProfile = %q, want work-prof", got)
	}
	if got := DiscoverProfile("claude", "/repo", "conv-123"); got != "" {
		t.Errorf("DiscoverProfile(claude) = %q, want empty", got)
	}
	if got := DiscoverProfile("antigravity", "/repo", "missing"); got != "" {
		t.Errorf("DiscoverProfile(missing) = %q, want empty", got)
	}
}

func TestDiscoverProfileAntigravitySkipsSymlinkedBrain(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sharedBrain := filepath.Join(tmpHome, ".gemini", "antigravity-cli", "brain")
	if err := os.MkdirAll(filepath.Join(sharedBrain, "conv-shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, prof := range []string{"aaa-first", "zzz-second"} {
		cliDir := filepath.Join(tmpHome, ".harness", "profiles", "antigravity", prof, "home", ".gemini", "antigravity-cli")
		if err := os.MkdirAll(cliDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sharedBrain, filepath.Join(cliDir, "brain")); err != nil {
			t.Fatal(err)
		}
	}
	if got := DiscoverProfile("antigravity", "/repo", "conv-shared"); got != "" {
		t.Errorf("DiscoverProfile with symlinked brain = %q, want empty (must not guess first alphabetical profile)", got)
	}
}

func TestNormalizeAndDisplayApp(t *testing.T) {
	if got := Normalize("agy"); got != "antigravity" {
		t.Errorf("Normalize(agy) = %q", got)
	}
	if got := Normalize("/usr/local/bin/claude"); got != "claude" {
		t.Errorf("Normalize(claude path) = %q", got)
	}
	if got := DisplayApp("claude"); got != "claude-code" {
		t.Errorf("DisplayApp(claude) = %q", got)
	}
	if got := DisplayApp("codex"); got != "codex" {
		t.Errorf("DisplayApp(codex) = %q", got)
	}
}
