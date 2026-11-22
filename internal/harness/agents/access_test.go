package agents

import "testing"

func TestAccessLevelsOrderAndIDs(t *testing.T) {
	// Level-capable agents: every table carries exactly one "default" and
	// unique IDs.
	expect := map[string][]string{
		"claude":      {"read-only", "default", "auto", "full"},
		"codex":       {"read-only", "default", "auto", "full"},
		"grok":        {"default", "full"},
		"opencode":    {"default", "auto"},
		"copilot":     {"default", "full"},
		"kimi":        {"default", "auto", "full"},
		"kimi-cli":    {"default", "full"},
		"qwen":        {"read-only", "default", "auto", "full"},
		"gemini":      {"read-only", "default", "auto", "full"},
		"muse":        {"untrusted", "default", "full"},
		"kilo":        {"default", "auto"},
		"antigravity": {"read-only", "sandboxed", "default", "accept-edits", "auto", "full"},
		"cline":       {"read-only", "ask", "default", "auto"},
		"fx":          {"ask", "default", "auto", "full"},
	}
	for agent, ids := range expect {
		levels := AccessLevels(agent)
		got := make([]string, len(levels))
		defaults := 0
		for i, level := range levels {
			got[i] = level.ID
			if level.ID == "default" {
				defaults++
				if len(level.Args) != 0 {
					t.Errorf("%s default level must not add flags, got %v", agent, level.Args)
				}
			}
			if level.Label == "" || level.Desc == "" {
				t.Errorf("%s level %s lacks label/description", agent, level.ID)
			}
		}
		if defaults != 1 {
			t.Errorf("%s has %d default levels, want 1", agent, defaults)
		}
		if len(got) != len(ids) {
			t.Fatalf("%s levels = %v, want %v", agent, got, ids)
		}
		for i := range ids {
			if got[i] != ids[i] {
				t.Fatalf("%s levels = %v, want %v", agent, got, ids[i])
			}
		}
	}
}

func TestAccessLevelsUnknownAgents(t *testing.T) {
	for _, agent := range []string{"pi", "deepseek", "vibe", "mcode", "antigravity-native", "no-such-agent"} {
		if levels := AccessLevels(agent); len(levels) != 0 {
			t.Errorf("%s unexpectedly has access levels: %v", agent, levels)
		}
	}
}

func TestAccessLevelFlags(t *testing.T) {
	cases := []struct {
		agent, id string
		args      []string
	}{
		{"claude", "read-only", []string{"--permission-mode", "plan"}},
		{"claude", "auto", []string{"--permission-mode", "acceptEdits"}},
		{"claude", "full", []string{"--dangerously-skip-permissions"}},
		{"codex", "read-only", []string{"--sandbox", "read-only"}},
		{"codex", "auto", []string{"--sandbox", "workspace-write", "--ask-for-approval", "on-failure"}},
		{"codex", "full", []string{"--dangerously-bypass-approvals-and-sandbox"}},
		{"grok", "full", []string{"--always-approve"}},
		{"opencode", "auto", []string{"--auto"}},
		{"copilot", "full", []string{"--allow-all"}},
		{"kimi", "auto", []string{"--yolo"}},
		{"kimi", "full", []string{"--auto"}},
		{"kimi-cli", "full", []string{"--yolo"}},
		{"qwen", "full", []string{"--approval-mode", "yolo"}},
		{"gemini", "auto", []string{"--approval-mode", "auto_edit"}},
		{"muse", "full", []string{"--approval-mode", "never"}},
		{"kilo", "auto", []string{"--auto"}},
		{"antigravity", "read-only", []string{"--mode", "plan"}},
		{"antigravity", "sandboxed", []string{"--sandbox"}},
		{"antigravity", "accept-edits", []string{"--mode", "accept-edits"}},
		{"antigravity", "auto", []string{"--mode", "auto"}},
		{"antigravity", "full", []string{"--dangerously-skip-permissions"}},
		{"cline", "read-only", []string{"--plan"}},
		{"cline", "ask", []string{"--auto-approve", "false"}},
		{"cline", "auto", []string{"--yolo"}},
		{"fx", "ask", []string{"--ask"}},
		{"fx", "auto", []string{"--auto"}},
		{"fx", "full", []string{"--full-access"}},
	}
	for _, tc := range cases {
		level, ok := FindAccessLevel(tc.agent, tc.id)
		if !ok {
			t.Fatalf("%s has no %s level", tc.agent, tc.id)
		}
		if len(level.Args) != len(tc.args) {
			t.Fatalf("%s %s args = %v, want %v", tc.agent, tc.id, level.Args, tc.args)
		}
		for i := range tc.args {
			if level.Args[i] != tc.args[i] {
				t.Fatalf("%s %s args = %v, want %v", tc.agent, tc.id, level.Args, tc.args)
			}
		}
	}
}

func TestFindAccessLevelMiss(t *testing.T) {
	if _, ok := FindAccessLevel("claude", "bogus"); ok {
		t.Fatal("bogus level must not be found")
	}
	if _, ok := FindAccessLevel("pi", "full"); ok {
		t.Fatal("pi has no levels, so full must not be found")
	}
}
