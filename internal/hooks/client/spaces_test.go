package client

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestDiscoverProfileAntigravity(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	brainDir := filepath.Join(tmpHome, ".model-router", "profiles", "antigravity", "work-prof", "home", ".gemini", "antigravity-cli", "brain", "conv-123")
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
