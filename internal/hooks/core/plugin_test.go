package core

import (
	"net/http"
	"strings"
	"testing"
)

func pluginEnvelope() map[string]any {
	return map[string]any{
		"v":       2,
		"agent":   "opencode",
		"session": "plug-sess",
		"pane":    "%12",
		"plugin": map[string]any{
			"name":         "opencode-spaces",
			"version":      "1.0.0",
			"capabilities": []any{"lifecycle", "usage", "context"},
		},
		"event":          "working",
		"ts_ms":          int64(1700000000000),
		"pid":            int64(12345),
		"harness_version": "1.2.3",
		"payload": map[string]any{
			"model":          "opus-4",
			"session_id":     "sess-1",
			"provider":       "anthropic",
			"mode":           "plan",
			"context_tokens": int64(8000),
			"context_window": int64(200000),
			"usage": map[string]any{
				"input_tokens":  int64(100),
				"output_tokens": int64(50),
			},
		},
	}
}

func TestNormalizePluginEnvelopeStamps(t *testing.T) {
	setupRuntime(t)
	ev := NormalizePluginEnvelope("opencode", pluginEnvelope())
	if ev == nil {
		t.Fatal("nil event")
	}
	if ev["source"] != "plugin" {
		t.Errorf("source = %v, want plugin", ev["source"])
	}
	if ev["plugin_name"] != "opencode-spaces" {
		t.Errorf("plugin_name = %v", ev["plugin_name"])
	}
	if ev["plugin_version"] != "1.0.0" {
		t.Errorf("plugin_version = %v", ev["plugin_version"])
	}
	if toInt64(ev["plugin_ts_ms"]) != 1700000000000 {
		t.Errorf("plugin_ts_ms = %v", ev["plugin_ts_ms"])
	}
	if toInt64(ev["pid"]) != 12345 {
		t.Errorf("pid = %v", ev["pid"])
	}
	if ev["harness_version"] != "1.2.3" {
		t.Errorf("harness_version = %v", ev["harness_version"])
	}
	if ev["provider"] != "anthropic" || ev["mode"] != "plan" {
		t.Errorf("rich session fields missing: %v", ev)
	}
	if ev["context_percent"] == nil {
		t.Errorf("context_percent should auto-derive")
	} else if f, _ := toFloat64(ev["context_percent"]); f < 3.9 || f > 4.1 {
		t.Errorf("context_percent = %v, want ~4", f)
	}
	if caps, ok := ev["plugin_capabilities"].([]any); !ok || len(caps) != 3 {
		t.Errorf("capabilities = %v", ev["plugin_capabilities"])
	}
}

func TestPluginRoutesAndPaneNormalization(t *testing.T) {
	setupRuntime(t)
	const token = "plug-token"
	srv := testServer(t, token)
	for _, path := range []string{"/v1/plugin/opencode", "/v1/plugin", "/v1/telemetry", "/v1/telemetry/opencode"} {
		body := `{"session":"plug-sess","pane":"%12","agent":"opencode","event":"working",` +
			`"ts_ms":1700000000000,"pid":1,"plugin":{"name":"p","version":"v","capabilities":[]},"payload":{"session_id":"s1"}}`
		if resp := postJSON(t, srv.URL+path, token, body); resp.StatusCode != http.StatusNoContent {
			t.Errorf("POST %s = %d, want 204", path, resp.StatusCode)
		}
	}
	// Pane key must be stored normalized without "%".
	store := LoadStore()
	sess, ok := store["panes"].(map[string]any)["plug-sess"].(map[string]any)
	if !ok {
		t.Fatal("session missing")
	}
	if _, ok := sess["12"]; !ok {
		t.Errorf("normalized pane key 12 missing, got %v", sess)
	}
	if _, ok := sess["%12"]; ok {
		t.Errorf("legacy %%12 key should not persist")
	}
	if mustGet(t, store, "panes", "plug-sess", "12")["source"] != "plugin" {
		t.Errorf("source not plugin")
	}
	// Telemetry endpoint requires token too.
	body := `{"session":"x","pane":1,"agent":"opencode","event":"working"}`
	if resp := postJSON(t, srv.URL+"/v1/plugin", "", body); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing token = %d, want 401", resp.StatusCode)
	}
}

func TestCompanionNormalizerSeam(t *testing.T) {
	setupRuntime(t)
	old := CompanionNormalizer
	defer func() { CompanionNormalizer = old }()
	CompanionNormalizer = func(agent string, envelope map[string]any) map[string]any {
		if agent != "claude" {
			return nil
		}
		return map[string]any{
			"v": 1, "agent": "claude", "app": "claude-code",
			"source": "plugin", "event": "custom", "state": "working",
			"text": "seam", "ts_ms": nowMs(), "seq": nowNanos(),
		}
	}
	const token = "seam-token"
	srv := testServer(t, token)
	body := `{"session":"seam","pane":3,"agent":"claude","event":"whatever","ts_ms":1700000000000}`
	if resp := postJSON(t, srv.URL+"/v1/plugin/claude", token, body); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := mustGet(t, LoadStore(), "panes", "seam", "3")["text"]; got != "seam" {
		t.Errorf("seam normalizer not used, text = %v", got)
	}
	// Nil return falls back to generic normalizer.
	CompanionNormalizer = func(agent string, envelope map[string]any) map[string]any { return nil }
	body2 := `{"session":"seam","pane":4,"agent":"pi","event":"working","ts_ms":1700000000000,"plugin":{"name":"n","version":"v","capabilities":[]}}`
	if resp := postJSON(t, srv.URL+"/v1/plugin/pi", token, body2); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("fallback status = %d", resp.StatusCode)
	}
	if got := mustGet(t, LoadStore(), "panes", "seam", "4")["source"]; got != "plugin" {
		t.Errorf("fallback source = %v", got)
	}
}

func TestPluginPrecedenceOverHook(t *testing.T) {
	setupRuntime(t)
	now := int64(1700000000000)
	Now = func() int64 { return now }
	// Seed a fresh plugin record.
	pev := NormalizePluginEnvelope("opencode", pluginEnvelope())
	if err := MergeIntoStore("plug-sess", "%12", pev); err != nil {
		t.Fatal(err)
	}
	Now = func() int64 { return now + 1000 }
	if !HasFreshPlugin("plug-sess", "12", now+1000) {
		t.Fatal("HasFreshPlugin = false, want true")
	}
	if !HasFreshPlugin("plug-sess", "%12", now+1000) {
		t.Fatal("HasFreshPlugin with %% pane = false, want true")
	}
	// Lower-authority hook must not overwrite plugin-owned fields.
	existing := mustGet(t, LoadStore(), "panes", "plug-sess", "12")
	hookEv := NormalizeEvent("opencode", "PreToolUse", map[string]any{
		"tool_name": "Bash", "session_id": "other",
		"model": "other-model",
		"source": "hook",
		"usage": map[string]any{"input_tokens": int64(9999)},
	}, map[string]string{})
	merged, applied := mergeEventApplied(existing, hookEv, now+1000)
	if applied {
		t.Errorf("hook over fresh plugin should return applied=false")
	}
	if merged["model"] != "opus-4" {
		t.Errorf("model overwritten: %v", merged["model"])
	}
	if merged["session_id"] != "sess-1" {
		t.Errorf("session_id overwritten: %v", merged["session_id"])
	}
	if merged["source"] != "plugin" {
		t.Errorf("source overwritten: %v", merged["source"])
	}
	// Once stale, hook merges as fallback.
	staleNow := now + WorkingStaleMS + 1000
	if HasFreshPlugin("plug-sess", 12, staleNow) {
		t.Errorf("stale plugin should not be fresh")
	}
	merged2, applied2 := mergeEventApplied(existing, hookEv, staleNow)
	if !applied2 {
		t.Errorf("stale plugin should allow hook fallback")
	}
	_ = merged2
}

func TestPluginSubagentIsolation(t *testing.T) {
	setupRuntime(t)
	now := int64(1700000000000)
	Now = func() int64 { return now }
	parent := NormalizePluginEnvelope("claude", map[string]any{
		"agent": "claude", "session": "s", "pane": 5,
		"event": "working", "ts_ms": now,
		"plugin": map[string]any{"name": "p", "version": "v", "capabilities": []any{}},
		"payload": map[string]any{
			"session_id": "parent-1", "model": "m-parent",
			"context_tokens": int64(100), "context_window": int64(1000),
			"title": "Parent title",
		},
	})
	if err := MergeIntoStore("s", 5, parent); err != nil {
		t.Fatal(err)
	}
	// Subagent-scoped plugin event.
	Now = func() int64 { return now + 5000 }
	sub := NormalizePluginEnvelope("claude", map[string]any{
		"agent": "claude", "session": "s", "pane": 5,
		"event": "working", "ts_ms": now + 5000,
		"is_subagent": true,
		"plugin": map[string]any{"name": "p", "version": "v", "capabilities": []any{}},
		"payload": map[string]any{
			"session_id": "child-9", "parent_session_id": "parent-1",
			"model": "m-child-should-not-win",
		},
	})
	// Ensure the isolation marker survives normalization.
	sub["is_subagent"] = true
	sub["parent_session_id"] = "parent-1"
	if err := MergeIntoStore("s", 5, sub); err != nil {
		t.Fatal(err)
	}
	rec := mustGet(t, LoadStore(), "panes", "s", "5")
	if rec["session_id"] != "parent-1" {
		t.Errorf("parent session_id overwritten: %v", rec["session_id"])
	}
	if rec["model"] != "m-parent" {
		t.Errorf("parent model overwritten: %v", rec["model"])
	}
	if rec["title"] != "Parent title" {
		t.Errorf("parent title overwritten: %v", rec["title"])
	}
	children, _ := rec["children"].([]any)
	if len(children) == 0 {
		t.Fatalf("subagent child not recorded: %v", rec)
	}
	found := false
	for _, raw := range children {
		if m, ok := raw.(map[string]any); ok && strings.TrimSpace(stringKey(m["id"])) == "child-9" {
			found = true
		}
	}
	if !found {
		t.Errorf("child-9 missing in %v", children)
	}
}

func TestPluginRichFieldsBoundedAndMerged(t *testing.T) {
	setupRuntime(t)
	now := int64(1700000000000)
	Now = func() int64 { return now }
	mkTools := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = map[string]any{"call_id": "c", "name": "t", "status": "running"}
		}
		return out
	}
	ev := NormalizePluginEnvelope("opencode", map[string]any{
		"agent": "opencode", "session": "s", "pane": 6,
		"event": "working", "ts_ms": now,
		"plugin": map[string]any{"name": "p", "version": "v", "capabilities": []any{}},
		"payload": map[string]any{
			"session_id":    "s1",
			"recent_tools":  mkTools(20),
			"active_tools":  mkTools(20),
			"files_touched": mkTools(40),
			"todos":         mkTools(40),
			"mcp_servers":   mkTools(40),
			"extras":        map[string]any{"agent_specific": "kept"},
			"context_tokens": int64(500),
			"context_window": int64(1000),
		},
	})
	if rt, ok := ev["recent_tools"].([]any); !ok || len(rt) != 16 {
		t.Errorf("recent_tools bounded to 16, got %d", len(rt))
	}
	if ft, ok := ev["files_touched"].([]any); !ok || len(ft) != 32 {
		t.Errorf("files_touched bounded to 32, got %d", len(ft))
	}
	if err := MergeIntoStore("s", 6, ev); err != nil {
		t.Fatal(err)
	}
	rec := mustGet(t, LoadStore(), "panes", "s", "6")
	if rt, _ := rec["recent_tools"].([]any); len(rt) != 16 {
		t.Errorf("stored recent_tools len = %d, want 16", len(rt))
	}
	if ex, ok := rec["extras"].(map[string]any); !ok || ex["agent_specific"] != "kept" {
		t.Errorf("extras not preserved: %v", rec["extras"])
	}
	if f, _ := toFloat64(rec["context_percent"]); f != 50 {
		t.Errorf("context_percent = %v, want 50", f)
	}
	sum := SpaceHooksSummary()
	pane := mustGet(t, sum, "panes", "s", "6")
	if pane["plugin_name"] != "p" {
		t.Errorf("summary missing plugin_name: %v", pane)
	}
	det := SpaceHooksDetail("s", "%6")
	if rec2, ok := det["record"].(map[string]any); !ok || rec2["extras"] == nil {
		t.Errorf("detail should preserve extras, got %v", det)
	}
}
