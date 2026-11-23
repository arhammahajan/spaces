package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

func richPluginDetail(nowMs int64) *model.HookDetail {
	return &model.HookDetail{
		HookSummary: model.HookSummary{
			Agent:               "claude",
			App:                 "claude",
			State:               "working",
			Event:               "turn.update",
			Source:              "plugin",
			PluginName:          "tmux-companion",
			PluginVersion:       "0.3.1",
			HarnessVersion:      "1.2.3",
			PID:                 1234,
			Provider:            "anthropic",
			Model:               "claude-opus-4",
			Effort:              "high",
			Mode:                "code",
			SandboxMode:         "workspace-write",
			GitBranch:           "feat/plugin-telemetry",
			SessionID:           "sess-123",
			ContextTokens:       42_500,
			ContextWindow:       200_000,
			ContextPercent:      21.25,
			TurnCount:           5,
			CompactionCount:     2,
			LastCompactionReason: "auto",
			ToolCallCount:       12,
			ToolErrorCount:      1,
			RetryCount:          2,
			Usage: &model.HookUsage{
				InputTokens:      12_400,
				OutputTokens:     3_100,
				ReasoningTokens:  400,
				CacheReadTokens:  8_200,
				CacheWriteTokens: 1_500,
				CostUSD:          0.04,
			},
			RateLimited:      true,
			RateLimitMessage: "slow down",
			RateLimitResetMs: nowMs + 45_000,
			LastError:        "something broke",
			PendingPermission: &model.HookPendingInteraction{
				Kind:        "permission",
				Tool:        "Bash",
				Title:       "Allow command?",
				Description: "Run go test ./...",
				Options:     []string{"Allow", "Deny"},
			},
			ActiveTools: []model.HookToolExecution{
				{Name: "Bash", Status: "running", Input: "go test ./...", StartedMs: nowMs - 14_000},
			},
			RecentTools: []model.HookToolExecution{
				{Name: "Edit", Status: "completed", DurationMs: 8_000},
				{Name: "Bash", Status: "failed", DurationMs: 2_000, Error: "exit 1"},
			},
			FilesTouched: []model.HookFileTouch{
				{Path: "internal/tui/view.go", Action: "edit", Additions: 10, Deletions: 2},
			},
			Todos: []model.HookTodoItem{
				{ID: "1", Content: "Implement plugin detail", Status: "in_progress"},
				{ID: "2", Content: "Write tests", Status: "pending"},
				{ID: "3", Content: "Done thing", Status: "completed"},
			},
			MCPServers: []model.HookMCPServerStatus{
				{Name: "fs", Status: "connected", Tools: 3},
				{Name: "gh", Status: "error", Error: "auth failed"},
			},
			LSPDiagnostics: &model.HookLSPDiagnostics{Errors: 2, Warnings: 1, Files: 1},
		},
		Prompt: "implement phase 2c",
		Reply:  "working on it",
	}
}

func TestHookDetailRendersRichPluginTelemetry(t *testing.T) {
	fixedNow := time.UnixMilli(1700000500000)
	nowMs := fixedNow.UnixMilli()
	spaces := []model.Space{
		{
			Name:      "work",
			IsCurrent: true,
			Status:    model.StatusWorking,
			Panes: []model.Pane{
				{
					ID: "%1", NumericID: 1, WindowID: "@1", WindowName: "claude",
					App: "claude", Command: "claude", Status: model.StatusWorking,
					Hook: &model.HookSummary{Agent: "claude", State: "working", HasDetail: true},
				},
			},
		},
	}
	detail := richPluginDetail(nowMs)
	fb := &fakeBackend{spaces: spaces, hook: detail}
	m := New(fb, Options{HomeDir: "/home/u", Now: func() time.Time { return fixedNow }})
	m.spaces = spaces
	m.width, m.height = 120, 40
	m.selected = 1
	u, _ := m.Update(keyRunes("H"))
	m = u.(Model)
	m.hookDetail = detail
	m.hookLoading = false

	plain := ansi.Strip(m.View())
	for _, want := range []string{
		"source & plugin",
		"tmux-companion",
		"0.3.1",
		"1.2.3",
		"1234",
		"anthropic",
		"high",
		"workspace-write",
		"feat/plugin-telemetry",
		"context:",
		"21%",
		"12.4k in",
		"3.1k out",
		"cache_r",
		"cache_w",
		"$0.04",
		"5 turns",
		"2 compactions",
		"auto",
		"12 tools",
		"1 errors",
		"2 retries",
		"rate limited",
		"slow down",
		"45s",
		"something broke",
		"pending permission",
		"Allow command?",
		"Allow / Deny",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("rich hook detail header missing %q:\n%s", want, plain)
		}
	}
	// Body sections live below the fold in the scrollable overlay: aggregate
	// across scroll offsets to verify full ModeHookDetail rendering.
	var scrolled strings.Builder
	for _, off := range []int{0, 8, 16, 24, 32, 48} {
		m.hookScroll = off
		scrolled.WriteString(ansi.Strip(m.View()))
		scrolled.WriteString("\n")
	}
	full := scrolled.String()
	for _, want := range []string{
		"active tools (1)",
		"Bash",
		"go test",
		"recent tools (2)",
		"exit 1",
		"files touched (1)",
		"internal/tui/view.go",
		"+10/-2",
		"todos 1/3 done",
		"Implement plugin detail",
		"mcp servers (2)",
		"auth failed",
		"2 errors",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("rich hook detail body missing %q:\n%s", want, full)
		}
	}
}

func TestHookDetailMinimalFallbackStaysClean(t *testing.T) {
	fixedNow := time.UnixMilli(1700000500000)
	spaces := []model.Space{
		{
			Name:      "work",
			IsCurrent: true,
			Status:    model.StatusWorking,
			Panes: []model.Pane{
				{
					ID: "%1", NumericID: 1, WindowID: "@1", WindowName: "claude",
					App: "claude", Command: "claude", Status: model.StatusWorking,
					Hook: &model.HookSummary{Agent: "claude", State: "working", HasDetail: true},
				},
			},
		},
	}
	detail := &model.HookDetail{
		HookSummary: model.HookSummary{Agent: "claude", App: "claude", State: "working", Model: "m1", SessionID: "s1", CWD: "/tmp"},
		Prompt:      "fix the failing test",
		Tool:        &model.HookTool{Name: "Bash", Input: "cargo test", Output: "ok"},
		Reply:       "looks good",
	}
	fb := &fakeBackend{spaces: spaces, hook: detail}
	m := New(fb, Options{HomeDir: "/home/u", Now: func() time.Time { return fixedNow }})
	m.spaces = spaces
	m.width, m.height = 120, 40
	m.selected = 1
	u, _ := m.Update(keyRunes("H"))
	m = u.(Model)
	m.hookDetail = detail
	m.hookLoading = false

	plain := ansi.Strip(m.View())
	for _, want := range []string{"fix the failing test", "cargo test", "model", "session"} {
		if !strings.Contains(plain, want) {
			t.Errorf("minimal hook detail missing %q:\n%s", want, plain)
		}
	}
	for _, banned := range []string{
		"source & plugin",
		"model & session",
		"context & usage",
		"context:",
		"rate limited",
		"pending permission",
		"active tools",
		"recent tools",
		"files touched",
		"todos ",
		"mcp servers",
		"lsp:",
	} {
		if strings.Contains(plain, banned) {
			t.Errorf("minimal hook detail must not show %q:\n%s", banned, plain)
		}
	}
}

func TestHookInlinePill(t *testing.T) {
	if text, _ := hookInlinePillText(nil); text != "" {
		t.Fatalf("nil hook pill = %q, want empty", text)
	}
	if text, _ := hookInlinePillText(&model.HookSummary{ContextPercent: 42.4}); text != "ctx 42%" {
		t.Fatalf("context pill = %q, want ctx 42%%", text)
	}
	if text, _ := hookInlinePillText(&model.HookSummary{RateLimited: true, ContextPercent: 42}); text != "429" {
		t.Fatalf("rate-limited pill = %q, want 429", text)
	}
	if text, _ := hookInlinePillText(&model.HookSummary{Usage: &model.HookUsage{CostUSD: 0.04}}); text == "" {
		t.Fatal("cost pill should not be empty")
	}
	if text, _ := hookInlinePillText(&model.HookSummary{Agent: "claude"}); text != "" {
		t.Fatalf("plain hook pill = %q, want empty", text)
	}
}
