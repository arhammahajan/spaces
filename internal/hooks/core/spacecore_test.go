package core

import (
	"os"
	"path/filepath"
	"testing"
)

// setupRuntime points all runtime paths into a temp dir and freezes the
// clock, mirroring the Python test Runtime helper. Every test that touches
// the store, spool or server must call it first: no real home access, no
// real zellij, no sleeping for TTLs.
func setupRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SPACES_HOOKS_RUNTIME", dir)
	t.Setenv("SPACES_HOOKS_STORE", filepath.Join(dir, "hooks.store.json"))
	t.Setenv("SPACES_HOOKS_NO_DAEMON", "1")
	t.Setenv("SPACES_HOOKS_DISABLE", "")
	t.Setenv("ZJ_HOOK_AGENT", "")
	t.Setenv("GROK_SESSION_ID", "")
	t.Setenv("GROK_HOOK_EVENT", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	fixed := int64(1700000000000)
	old := Now
	Now = func() int64 { return fixed }
	t.Cleanup(func() { Now = old })
	return dir
}

func mustGet(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("key %q missing or not a map in %v", k, cur)
		}
		cur = next
	}
	return cur
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
