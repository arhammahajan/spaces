package plugins

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/arhammahajan/spaces/internal/agent"
)

func TestProviderResumeArgs(t *testing.T) {
	reg := agent.NewRegistry()
	RegisterAll(reg)

	cases := map[string][]string{
		"antigravity":  {"--conversation", "s1"},
		"claude":       {"--resume", "s1"},
		"codex":        {"resume", "s1"},
		"cursor-agent": {"--resume", "s1"},
		"deepseek":     {"--session", "s1"},
		"kilo":         {"--session", "s1"},
		"kimi":         {"--session", "s1"},
		"muse":         {"resume", "s1"},
		"opencode":     {"--session", "s1"},
	}
	for id, want := range cases {
		got := reg.ResumeArgs(id, "s1")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ResumeArgs(%q, s1) = %v, want %v", id, got, want)
		}
		if empty := reg.ResumeArgs(id, ""); len(empty) != 0 {
			t.Errorf("ResumeArgs(%q, empty) = %v, want empty", id, empty)
		}
	}
}

func TestDiscoverProfileRootsAndFindUnclaimedConversation(t *testing.T) {
	reg := agent.NewRegistry()
	RegisterAll(reg)

	home := t.TempDir()
	// Default home Claude session
	defaultClaudeDir := filepath.Join(home, ".claude", "projects", "repo")
	if err := os.MkdirAll(defaultClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultClaudeDir, "sess-default.jsonl"), []byte(
		`{"type":"user","sessionId":"sess-default","cwd":"/work/repo","timestamp":"2026-11-22T10:00:00Z","message":{"role":"user","content":"Fix login"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	// Harness profile home for claude/work
	profHome := filepath.Join(home, ".model-router", "profiles", "claude", "work", "home")
	profClaudeDir := filepath.Join(profHome, ".claude", "projects", "repo")
	if err := os.MkdirAll(profClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profClaudeDir, "sess-work.jsonl"), []byte(
		`{"type":"user","sessionId":"sess-work","cwd":"/work/repo","timestamp":"2026-11-22T11:00:00Z","message":{"role":"user","content":"Build plugin split"}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	roots := reg.DiscoverProfileRoots(home)
	if len(roots) != 1 || roots[0].Agent != "claude" || roots[0].Profile != "work" || roots[0].HomeDir != profHome {
		t.Fatalf("DiscoverProfileRoots = %+v, want 1 claude/work root", roots)
	}

	// FindUnclaimedConversation with profile="work" finds sess-work
	sid, ok := reg.FindUnclaimedConversation(context.Background(), home, "claude", "/work/repo", "work", "", nil)
	if !ok || sid != "sess-work" {
		t.Fatalf("FindUnclaimedConversation(work) = (%q, %v), want (sess-work, true)", sid, ok)
	}

	// Excluding sess-work with no profile falls back to sess-default
	sid, ok = reg.FindUnclaimedConversation(context.Background(), home, "claude", "/work/repo", "", "", map[string]bool{"sess-work": true})
	if !ok || sid != "sess-default" {
		t.Fatalf("FindUnclaimedConversation(exclude work) = (%q, %v), want (sess-default, true)", sid, ok)
	}

	// Excluding both returns ("", true)
	sid, ok = reg.FindUnclaimedConversation(context.Background(), home, "claude", "/work/repo", "", "", map[string]bool{"sess-work": true, "sess-default": true})
	if !ok || sid != "" {
		t.Fatalf("FindUnclaimedConversation(exclude all) = (%q, %v), want ('', true)", sid, ok)
	}
}
