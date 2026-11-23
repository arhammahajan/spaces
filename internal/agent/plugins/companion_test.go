package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/agent"
	pluginsfs "github.com/arhammahajan/spaces/plugins"
)

func companionByID(t *testing.T, id string) agent.CompanionPlugin {
	t.Helper()
	cp := agent.DefaultRegistry().CompanionPlugin(id)
	if cp == nil {
		t.Fatalf("CompanionPlugin(%q) = nil", id)
	}
	return cp
}

func TestCompanionPluginsRegistered(t *testing.T) {
	got := map[string]bool{}
	for _, p := range agent.DefaultRegistry().CompanionPlugins() {
		got[p.Spec().ID] = true
	}
	for _, id := range []string{"opencode", "pi", "claude", "codex"} {
		if !got[id] {
			t.Errorf("CompanionPlugins() missing %q (have %v)", id, got)
		}
	}
}

func TestCompanionEmbedFS(t *testing.T) {
	for _, f := range []string{
		"opencode-spaces/index.mjs",
		"opencode-spaces/tracker.mjs",
		"opencode-spaces/transport.mjs",
		"opencode-spaces/package.json",
		"pi-spaces/index.ts",
		"pi-spaces/tracker.ts",
		"pi-spaces/transport.ts",
		"pi-spaces/package.json",
		"claude-spaces/hooks/hooks.json",
		"claude-spaces/scripts/hook-bridge.mjs",
		"claude-spaces/scripts/statusline-bridge.mjs",
		"claude-spaces/.claude-plugin/plugin.json",
		"codex-spaces/hooks.json",
		"codex-spaces/scripts/hook-bridge.mjs",
		"codex-spaces/scripts/otel-bridge.mjs",
	} {
		if _, err := fs.Stat(pluginsfs.FS, f); err != nil {
			t.Errorf("embedded %s missing: %v", f, err)
		}
	}
}

func companionBaseEnvelope(agentID, event string, payload map[string]any) map[string]any {
	return map[string]any{
		"v": 2, "agent": agentID, "session": "testsess", "pane": "%1",
		"plugin": map[string]any{
			"name": agentID + "-spaces", "version": "1.0.0",
			"capabilities": []any{"lifecycle"},
		},
		"event": event, "ts_ms": int64(1795444000000), "pid": int64(4242),
		"payload": payload,
	}
}

func mustState(t *testing.T, out map[string]any, wantState, wantKind string) {
	t.Helper()
	if out["source"] != "plugin" {
		t.Errorf("source = %v, want plugin", out["source"])
	}
	if out["state"] != wantState {
		t.Errorf("state = %v, want %q", out["state"], wantState)
	}
	gotKind, _ := out["kind"].(string)
	if wantKind == "" {
		if out["kind"] != nil && gotKind != "" {
			t.Errorf("kind = %v, want nil", out["kind"])
		}
	} else if gotKind != wantKind {
		t.Errorf("kind = %v, want %q", out["kind"], wantKind)
	}
}

func TestOpenCodeNormalizeCompanionEvent(t *testing.T) {
	cp := companionByID(t, "opencode")
	if spec := cp.CompanionSpec(); spec.AgentID != "opencode" || spec.PluginName != "opencode-spaces" ||
		spec.Version != "1.0.0" || spec.InstallDir != ".config/opencode/plugins/opencode-spaces" || spec.Runtime != "node/esm" {
		t.Fatalf("spec = %+v", spec)
	}

	env := companionBaseEnvelope("opencode", "permission.asked", map[string]any{
		"session_id": "ses_123", "title": "implement feature", "cwd": "/repo",
		"provider": "anthropic", "model": "claude-opus-4-6", "effort": "high", "mode": "build",
		"state": "blocked", "kind": "permission",
		"text": "approve Edit · src/a.ts",
		"tool": "Edit", "tool_input": `{"file_path":"src/a.ts"}`,
		"usage":          map[string]any{"input_tokens": 1000, "output_tokens": 500, "cost_usd": 0.012},
		"context_tokens": int64(50000), "context_window": int64(200000),
		"files_touched":      []any{map[string]any{"path": "src/a.ts"}},
		"todos":              []any{map[string]any{"content": "write code", "status": "in_progress"}},
		"mcp_servers":        []any{map[string]any{"name": "gh", "status": "ok"}},
		"lsp_diagnostics":    map[string]any{"errors": 1, "warnings": 0, "files": 1},
		"pending_permission": map[string]any{"tool": "Edit"},
		"pending_question":   map[string]any{"question": "proceed?"},
		"subagents":          []any{map[string]any{"id": "sub1", "state": "working", "text": "explore"}},
	})
	out, ok := cp.NormalizeCompanionEvent(env)
	if !ok {
		t.Fatal("NormalizeCompanionEvent ok=false")
	}
	mustState(t, out, "blocked", "permission")
	if out["agent"] != "opencode" {
		t.Errorf("agent = %v", out["agent"])
	}
	if u, ok := out["usage"].(map[string]any); !ok || companionInt64(u["input_tokens"]) != 1000 || u["cost_usd"] != 0.012 {
		t.Errorf("usage = %v", out["usage"])
	}
	if pct, _ := out["context_percent"].(float64); pct < 24.9 || pct > 25.1 {
		t.Errorf("context_percent = %v, want 25", out["context_percent"])
	}
	if sl, _ := out["subagents"].([]any); len(sl) != 1 {
		t.Errorf("subagents = %v", out["subagents"])
	}
	if out["pending_question"] == nil || out["pending_permission"] == nil {
		t.Errorf("pending markers dropped: %v", out)
	}
	if out["mcp_servers"] == nil || out["lsp_diagnostics"] == nil {
		t.Errorf("mcp/lsp dropped: %v", out)
	}
	if out["model"] != "claude-opus-4-6" || out["provider"] != "anthropic" {
		t.Errorf("model/provider = %v/%v", out["model"], out["provider"])
	}

	// Subagent isolation: child traffic is marked and never unmarked.
	sub := companionBaseEnvelope("opencode", "tool.execute.before", map[string]any{
		"session_id": "sub1", "parent_session_id": "ses_123", "is_subagent": true,
		"state": "working", "tool": "Read", "model": "child-model",
	})
	sout, ok := cp.NormalizeCompanionEvent(sub)
	if !ok || sout["is_subagent"] != true || sout["parent_session_id"] != "ses_123" {
		t.Errorf("subagent markers = (%v, %v), ok=%v", sout["is_subagent"], sout["parent_session_id"], ok)
	}

	// Event-name fallback when the payload carries no explicit state.
	idle := companionBaseEnvelope("opencode", "session.idle", map[string]any{"session_id": "ses_123"})
	iout, ok := cp.NormalizeCompanionEvent(idle)
	if !ok {
		t.Fatal("idle ok=false")
	}
	mustState(t, iout, "idle", "")

	if _, ok := cp.NormalizeCompanionEvent(nil); ok {
		t.Error("nil envelope should return ok=false")
	}
	if _, ok := cp.NormalizeCompanionEvent(companionBaseEnvelope("claude", "Stop", map[string]any{})); ok {
		t.Error("foreign agent envelope should return ok=false")
	}
}

func TestPiNormalizeCompanionEvent(t *testing.T) {
	cp := companionByID(t, "pi")
	if spec := cp.CompanionSpec(); spec.AgentID != "pi" || spec.PluginName != "pi-spaces" ||
		spec.Version != "1.0.0" || spec.InstallDir != ".pi/agent/extensions/pi-spaces" || spec.Runtime != "pi/ts" {
		t.Fatalf("spec = %+v", spec)
	}

	env := companionBaseEnvelope("pi", "tool_call", map[string]any{
		"session_id": "pi-ses-1", "title": "refactor", "cwd": "/repo",
		"provider": "anthropic", "model": "claude-sonnet", "effort": "medium", "mode": "default",
		"state": "working", "text": "Edit · src/b.ts",
		"tool": "edit", "tool_input": "src/b.ts",
		"usage":          map[string]any{"input_tokens": 2000, "output_tokens": 300, "cost_usd": 0.004},
		"context_tokens": int64(40000), "context_window": int64(200000), "context_percent": 20.0,
		"turn_count": int64(3), "tool_call_count": int64(7),
		"active_tools":   []any{map[string]any{"tool": "edit"}},
		"recent_tools":   []any{map[string]any{"tool": "read", "ok": true}},
		"files_touched":  []any{map[string]any{"path": "src/b.ts", "action": "edit"}},
		"thinking_level": "high",
	})
	out, ok := cp.NormalizeCompanionEvent(env)
	if !ok {
		t.Fatal("ok=false")
	}
	mustState(t, out, "working", "")
	if out["thinking_level"] != "high" {
		t.Errorf("thinking_level = %v", out["thinking_level"])
	}
	if out["effort"] != "medium" || out["model"] != "claude-sonnet" {
		t.Errorf("effort/model = %v/%v", out["effort"], out["model"])
	}
	if sl, _ := out["active_tools"].([]any); len(sl) != 1 {
		t.Errorf("active_tools = %v", out["active_tools"])
	}
	if sl, _ := out["files_touched"].([]any); len(sl) != 1 {
		t.Errorf("files_touched = %v", out["files_touched"])
	}
	if u, ok := out["usage"].(map[string]any); !ok || companionInt64(u["output_tokens"]) != 300 {
		t.Errorf("usage = %v", out["usage"])
	}

	end := companionBaseEnvelope("pi", "turn_end", map[string]any{"session_id": "pi-ses-1"})
	eout, ok := cp.NormalizeCompanionEvent(end)
	if !ok {
		t.Fatal("turn_end ok=false")
	}
	mustState(t, eout, "done", "")

	shut := companionBaseEnvelope("pi", "session_shutdown", map[string]any{"session_id": "pi-ses-1"})
	sout, ok := cp.NormalizeCompanionEvent(shut)
	if !ok {
		t.Fatal("shutdown ok=false")
	}
	if sout["state"] != "idle" && sout["state"] != "done" {
		t.Errorf("shutdown state = %v", sout["state"])
	}
}

func TestClaudeNormalizeCompanionEvent(t *testing.T) {
	cp := companionByID(t, "claude")
	if spec := cp.CompanionSpec(); spec.AgentID != "claude" || spec.PluginName != "claude-spaces" ||
		spec.Version != "1.0.0" || spec.InstallDir != ".claude/plugins/claude-spaces" || spec.Runtime != "node/esm" {
		t.Fatalf("spec = %+v", spec)
	}

	// Ask-tool gate: PreToolUse with an ask tool blocks on a question.
	ask := companionBaseEnvelope("claude", "PreToolUse", map[string]any{
		"session_id": "c-ses", "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"questions": "which?"},
	})
	aout, ok := cp.NormalizeCompanionEvent(ask)
	if !ok {
		t.Fatal("ask ok=false")
	}
	mustState(t, aout, "blocked", "question")

	// statusLine live metrics event.
	status := companionBaseEnvelope("claude", "statusline", map[string]any{
		"session_id": "c-ses", "cwd": "/repo", "model": "claude-opus-4-6", "mode": "default",
		"state": "working", "text": "claude-opus-4-6 · repo · ctx 42%",
		"usage":          map[string]any{"input_tokens": 80000, "output_tokens": 4000, "cache_read_tokens": 10000, "cache_write_tokens": 2000, "cost_usd": 1.23},
		"context_tokens": int64(96000), "context_window": int64(200000), "context_percent": 48.0,
		"files_touched":    []any{"src/a.ts"},
		"todos":            []any{map[string]any{"content": "x", "status": "pending"}},
		"background_tasks": []any{map[string]any{"id": "bg1", "state": "working"}},
	})
	sout, ok := cp.NormalizeCompanionEvent(status)
	if !ok {
		t.Fatal("statusline ok=false")
	}
	mustState(t, sout, "working", "")
	if sout["cost_usd"] != 1.23 {
		t.Errorf("cost_usd = %v", sout["cost_usd"])
	}
	if sout["context_tokens"] != int64(96000) || sout["context_window"] != int64(200000) {
		t.Errorf("context = %v/%v", sout["context_tokens"], sout["context_window"])
	}
	if sl, _ := outSlice(sout, "background_tasks"); len(sl) != 1 {
		t.Errorf("background_tasks = %v", sout["background_tasks"])
	}

	// Subagent isolation via agent_id.
	sub := companionBaseEnvelope("claude", "SubagentStart", map[string]any{
		"session_id": "c-sub", "agent_id": "a11", "agent_type": "Explore",
		"description": "explore repo",
	})
	subOut, ok := cp.NormalizeCompanionEvent(sub)
	if !ok {
		t.Fatal("subagent ok=false")
	}
	if subOut["is_subagent"] != true || subOut["agent_id"] != "a11" || subOut["agent_type"] != "Explore" {
		t.Errorf("subagent markers = %v", subOut)
	}

	// Permission + auth + failure branches.
	perm := companionBaseEnvelope("claude", "PermissionRequest", map[string]any{
		"session_id": "c-ses", "tool_name": "Bash",
	})
	pout, _ := cp.NormalizeCompanionEvent(perm)
	mustState(t, pout, "blocked", "permission")

	auth := companionBaseEnvelope("claude", "PermissionRequest", map[string]any{
		"session_id": "c-ses", "message": "Please sign in to continue",
	})
	authOut, _ := cp.NormalizeCompanionEvent(auth)
	mustState(t, authOut, "blocked", "auth")

	stop := companionBaseEnvelope("claude", "Stop", map[string]any{"session_id": "c-ses"})
	stopOut, _ := cp.NormalizeCompanionEvent(stop)
	mustState(t, stopOut, "done", "")

	stopErr := companionBaseEnvelope("claude", "Stop", map[string]any{"session_id": "c-ses", "error": "boom"})
	stopErrOut, _ := cp.NormalizeCompanionEvent(stopErr)
	mustState(t, stopErrOut, "failed", "")

	fail := companionBaseEnvelope("claude", "PostToolUseFailure", map[string]any{"session_id": "c-ses", "tool_name": "Edit"})
	failOut, _ := cp.NormalizeCompanionEvent(fail)
	mustState(t, failOut, "failed", "")
}

func outSlice(out map[string]any, key string) ([]any, bool) {
	sl, ok := out[key].([]any)
	return sl, ok
}

func TestCodexNormalizeCompanionEvent(t *testing.T) {
	cp := companionByID(t, "codex")
	if spec := cp.CompanionSpec(); spec.AgentID != "codex" || spec.PluginName != "codex-spaces" ||
		spec.Version != "1.0.0" || spec.InstallDir != ".codex/plugins/codex-spaces" || spec.Runtime != "node/esm" {
		t.Fatalf("spec = %+v", spec)
	}

	// Modern hooks.json PermissionRequest.
	perm := companionBaseEnvelope("codex", "PermissionRequest", map[string]any{
		"session_id": "thread-1", "tool_name": "shell", "model": "gpt-5.4", "effort": "high",
		"sandbox_mode": "workspace-write",
	})
	pout, ok := cp.NormalizeCompanionEvent(perm)
	if !ok {
		t.Fatal("perm ok=false")
	}
	mustState(t, pout, "blocked", "permission")
	if pout["sandbox_mode"] != "workspace-write" || pout["effort"] != "high" {
		t.Errorf("sandbox/effort = %v/%v", pout["sandbox_mode"], pout["effort"])
	}

	// OTLP codex.* telemetry with rate limiting.
	otel := companionBaseEnvelope("codex", "codex.tool_result", map[string]any{
		"session_id": "thread-1", "model": "gpt-5.4", "tool": "shell",
		"usage":              map[string]any{"input_tokens": 5000, "output_tokens": 700},
		"context_tokens":     int64(12000),
		"context_window":     int64(272000),
		"rate_limited":       true,
		"rate_limit_message": "HTTP 429",
		"extras":             map[string]any{"otel_signal": "/v1/logs", "otel_events": []any{"codex.tool_result"}},
	})
	otelOut, ok := cp.NormalizeCompanionEvent(otel)
	if !ok {
		t.Fatal("otel ok=false")
	}
	mustState(t, otelOut, "working", "")
	if otelOut["rate_limited"] != true || otelOut["rate_limit_message"] != "HTTP 429" {
		t.Errorf("rate limit = %v/%v", otelOut["rate_limited"], otelOut["rate_limit_message"])
	}
	if ex, ok := otelOut["extras"].(map[string]any); !ok || ex["otel_signal"] != "/v1/logs" {
		t.Errorf("extras = %v", otelOut["extras"])
	}

	// Legacy notify agent-turn-complete.
	legacy := companionBaseEnvelope("codex", "agent-turn-complete", map[string]any{
		"session_id": "thread-1", "type": "agent-turn-complete",
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20},
	})
	legacyOut, ok := cp.NormalizeCompanionEvent(legacy)
	if !ok {
		t.Fatal("legacy ok=false")
	}
	mustState(t, legacyOut, "done", "")

	// Subagent isolation.
	sub := companionBaseEnvelope("codex", "PreToolUse", map[string]any{
		"session_id": "child-9", "parent_thread_id": "thread-1", "agent_id": "child-9",
	})
	subOut, ok := cp.NormalizeCompanionEvent(sub)
	if !ok {
		t.Fatal("sub ok=false")
	}
	if subOut["is_subagent"] != true || subOut["agent_id"] != "child-9" {
		t.Errorf("subagent markers = %v", subOut)
	}
}

func TestCompanionInstallDetectUninstall(t *testing.T) {
	type tc struct {
		id           string
		installRel   string
		marker       string
		execScript   string
		extraChecks  func(t *testing.T, home, dir string)
		absentChecks func(t *testing.T, home, dir string)
	}
	cases := []tc{
		{
			id: "opencode", installRel: ".config/opencode/plugins/opencode-spaces",
			marker: "index.mjs",
			extraChecks: func(t *testing.T, home, dir string) {
				t.Helper()
				wrapper := filepath.Join(home, ".config", "opencode", "plugins", "opencode-spaces.mjs")
				data, err := os.ReadFile(wrapper)
				if err != nil {
					t.Fatalf("wrapper missing: %v", err)
				}
				if !strings.Contains(string(data), "./opencode-spaces/index.mjs") {
					t.Errorf("wrapper does not re-export index.mjs:\n%s", data)
				}
			},
			absentChecks: func(t *testing.T, home, dir string) {
				t.Helper()
				if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "plugins", "opencode-spaces.mjs")); !os.IsNotExist(err) {
					t.Errorf("wrapper not removed")
				}
			},
		},
		{
			id: "pi", installRel: ".pi/agent/extensions/pi-spaces", marker: "index.ts",
		},
		{
			id: "claude", installRel: ".claude/plugins/claude-spaces",
			marker: "scripts/hook-bridge.mjs", execScript: "scripts/hook-bridge.mjs",
			extraChecks: func(t *testing.T, home, dir string) {
				t.Helper()
				for _, f := range []string{"hooks/hooks.json", ".claude-plugin/plugin.json", "scripts/statusline-bridge.mjs"} {
					if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
						t.Errorf("%s missing: %v", f, err)
					}
				}
				if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
					t.Errorf("install must not create ~/.claude/settings.json")
				}
			},
		},
		{
			id: "codex", installRel: ".codex/plugins/codex-spaces",
			marker: "scripts/hook-bridge.mjs",
			extraChecks: func(t *testing.T, home, dir string) {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
				if err != nil {
					t.Fatalf("resolved hooks.json missing: %v", err)
				}
				if strings.Contains(string(data), "<PLUGIN_DIR>") {
					t.Errorf("resolved hooks.json still has <PLUGIN_DIR> placeholder")
				}
				if !strings.Contains(string(data), dir) {
					t.Errorf("resolved hooks.json lacks install dir %s", dir)
				}
				if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); !os.IsNotExist(err) {
					t.Errorf("install must not create ~/.codex/hooks.json")
				}
				if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); !os.IsNotExist(err) {
					t.Errorf("install must not create ~/.codex/config.toml")
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			home := t.TempDir()
			cp := companionByID(t, c.id)
			opts := agent.CompanionInstallOptions{HomeDir: home}
			wantDir := filepath.Join(home, filepath.FromSlash(c.installRel))

			st := cp.DetectCompanionStatus(home)
			if st.Installed {
				t.Fatalf("pre-install status = %+v, want not installed", st)
			}

			res, err := cp.InstallCompanion(opts)
			if err != nil {
				t.Fatalf("InstallCompanion: %v", err)
			}
			if res.Path != wantDir {
				t.Errorf("install path = %q, want %q", res.Path, wantDir)
			}
			if res.Version != "1.0.0" {
				t.Errorf("install version = %q", res.Version)
			}
			if _, err := os.Stat(filepath.Join(wantDir, filepath.FromSlash(c.marker))); err != nil {
				t.Errorf("marker %s missing: %v", c.marker, err)
			}
			if c.execScript != "" {
				fi, err := os.Stat(filepath.Join(wantDir, filepath.FromSlash(c.execScript)))
				if err != nil {
					t.Fatalf("exec script missing: %v", err)
				}
				if fi.Mode().Perm()&0o111 == 0 {
					t.Errorf("exec bit missing on %s (mode %v)", c.execScript, fi.Mode())
				}
			}
			if c.extraChecks != nil {
				c.extraChecks(t, home, wantDir)
			}

			st = cp.DetectCompanionStatus(home)
			if !st.Installed {
				t.Fatalf("post-install status = %+v, want installed", st)
			}
			if st.Path != wantDir || st.Version != "1.0.0" {
				t.Errorf("status = %+v", st)
			}
			if st.Detail == "" {
				t.Error("status Detail empty")
			}

			if err := cp.UninstallCompanion(opts); err != nil {
				t.Fatalf("UninstallCompanion: %v", err)
			}
			if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
				t.Errorf("install dir still present after uninstall")
			}
			if c.absentChecks != nil {
				c.absentChecks(t, home, wantDir)
			}
			st = cp.DetectCompanionStatus(home)
			if st.Installed {
				t.Errorf("post-uninstall status = %+v, want not installed", st)
			}
			// Second uninstall must be a clean no-op.
			if err := cp.UninstallCompanion(opts); err != nil {
				t.Errorf("repeat uninstall: %v", err)
			}
		})
	}
}
