package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stubExec(t *testing.T, fn ExecFunc) {
	t.Helper()
	old := ExecFn
	ExecFn = fn
	t.Cleanup(func() { ExecFn = old })
}

func TestSpacesBinaryHonorsEnv(t *testing.T) {
	t.Setenv("SPACES_BIN", "/custom/spaces")
	if got := SpacesBinary(); len(got) != 1 || got[0] != "/custom/spaces" {
		t.Errorf("got %v", got)
	}
}

func TestSpacesBinaryFallbackWithoutSpacesOnPath(t *testing.T) {
	t.Setenv("SPACES_BIN", "")
	t.Setenv("PATH", t.TempDir()) // no spaces here
	if got := SpacesBinary(); len(got) != 1 || got[0] != "tmux-spaces" {
		t.Errorf("got %v want [tmux-spaces]", got)
	}
}

func TestInProcessHooksRoundTrip(t *testing.T) {
	t.Setenv("SPACES_HOOKS_RUNTIME", t.TempDir())
	old := ExecFn
	ExecFn = nil
	t.Cleanup(func() { ExecFn = old })

	ctx := context.Background()
	entry := OutcomeEntry{
		Session:   "dev",
		Pane:      "3",
		Agent:     "claude",
		App:       "claude-code",
		Outcome:   "done",
		Text:      "all tests passed",
		SessionID: "sess-inproc-1",
		CWD:       "/repo/spaces",
		TsMs:      time.Now().UnixMilli(),
	}
	if err := RecordOutcome(ctx, entry); err != nil {
		t.Fatalf("RecordOutcome err = %v", err)
	}
	snap, err := FetchHooks(ctx)
	if err != nil {
		t.Fatalf("FetchHooks err = %v", err)
	}
	if !snap.OK || len(snap.Outcomes) != 1 || snap.Outcomes[0].SessionID != "sess-inproc-1" {
		t.Fatalf("unexpected snap = %+v", snap)
	}
	if err := RemoveOutcome(ctx, OutcomeEntry{Session: "dev", Agent: "claude", SessionID: "sess-inproc-1"}); err != nil {
		t.Fatalf("RemoveOutcome err = %v", err)
	}
	snap, err = FetchHooks(ctx)
	if err != nil {
		t.Fatalf("FetchHooks after remove err = %v", err)
	}
	if len(snap.Outcomes) != 0 {
		t.Fatalf("expected 0 outcomes after remove, got %+v", snap.Outcomes)
	}
}

func TestSpacesBinaryFindsSpacesOnPath(t *testing.T) {
	t.Setenv("SPACES_BIN", "")
	dir := t.TempDir()
	fake := filepath.Join(dir, "spaces")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got := SpacesBinary(); len(got) != 1 || got[0] != "spaces" {
		t.Errorf("got %v want [spaces]", got)
	}
}

func TestFetchHooks(t *testing.T) {
	payload := `{"ok":true,"version":1,"now_ms":10,"spool_pending":1,` +
		`"panes":{"dots":{"2":{"agent":"claude","app":"claude-code","state":"working",` +
		`"kind":null,"event":"pre_tool","ts_ms":9,"state_since_ms":9,"turn_started_ms":8,` +
		`"text":"Bash x cargo test","model":"m","session_id":"s","cwd":"/tmp","child_count":2,"has_detail":true}}}}`
	var gotName string
	var gotArgs []string
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte(payload), nil
	})
	snap, err := FetchHooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.OK || snap.Version != 1 || snap.SpoolPending != 1 {
		t.Errorf("meta = %+v", snap)
	}
	sum := snap.Panes["dots"]["2"]
	if sum.State != "working" || sum.ChildCount != 2 || sum.Model != "m" || !sum.HasDetail {
		t.Errorf("summary = %+v", sum)
	}
	if !sum.IsAuthBlock() && sum.State == "blocked" {
		t.Error("non-blocked state must not be auth block")
	}
	if gotArgs[len(gotArgs)-1] != "__space-hooks" {
		t.Errorf("args = %v", gotArgs)
	}
	_ = gotName
}

func TestFetchHooksError(t *testing.T) {
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("boom")
	})
	if _, err := FetchHooks(context.Background()); err == nil {
		t.Error("expected error")
	}
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("not json"), nil
	})
	if _, err := FetchHooks(context.Background()); err == nil {
		t.Error("expected decode error")
	}
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("  "), nil
	})
	if _, err := FetchHooks(context.Background()); err == nil {
		t.Error("expected empty-output error")
	}
}

func TestFetchHookDetail(t *testing.T) {
	payload := `{"ok":true,"version":1,"record":{"agent":"claude","app":"claude-code",` +
		`"state":"working","event":"pre_tool","ts_ms":9,"state_since_ms":9,"text":"t",` +
		`"prompt":"fix the test","tool":{"name":"Bash","input":"cargo test"},` +
		`"children":[{"id":"a1","type":"Explore","state":"working","since_ms":9,"text":"find it"}],` +
		`"background":0}}`
	var gotArgs []string
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(payload), nil
	})
	snap, err := FetchHookDetail(context.Background(), "dots", 2)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Record == nil || snap.Record.Prompt != "fix the test" {
		t.Fatalf("record = %+v", snap.Record)
	}
	if snap.Record.Tool == nil || snap.Record.Tool.Name != "Bash" {
		t.Errorf("tool = %+v", snap.Record.Tool)
	}
	if len(snap.Record.Children) != 1 || snap.Record.Children[0].Kind != "Explore" {
		t.Errorf("children = %+v", snap.Record.Children)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"__space-hooks", "--detail", "dots", "2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
}

func TestFetchHooksBackgroundWorkAndElapsed(t *testing.T) {
	payload := `{"ok":true,"version":1,"now_ms":20000,"spool_pending":0,` +
		`"panes":{"work":{"4":{"agent":"claude","app":"claude-code","state":"working",` +
		`"event":"stop","ts_ms":19000,"state_since_ms":1000,"turn_started_ms":1000,` +
		`"text":"waiting on: go test ./...","waiting_on":"waiting on: go test ./...",` +
		`"child_count":1,"background":2,"finite_active":2,"service_active":1,"has_detail":true,` +
		`"children":[{"id":"sub-1","type":"Explore","kind":"subagent","category":"finite","state":"working","since_ms":5000,"ts_ms":15000,"text":"search"}],` +
		`"background_tasks":[` +
		`{"id":"bg-1","type":"local_bash","kind":"command","category":"finite","state":"working","command":"go test ./...","since_ms":10000,"ts_ms":18000,"text":"go test ./..."},` +
		`{"id":"bg-2","type":"local_bash","kind":"service","category":"service","state":"working","command":"npm run dev","since_ms":2000,"ts_ms":18000,"text":"npm run dev"},` +
		`{"id":"bg-3","type":"local_bash","kind":"command","category":"finite","state":"done","outcome":"exit 0","command":"go vet ./...","since_ms":3000,"ts_ms":8000,"ended_ms":8000,"text":"go vet ./..."}` +
		`]}}}}`
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(payload), nil
	})
	snap, err := FetchHooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sum := snap.Panes["work"]["4"]
	if sum.WaitingOn != "waiting on: go test ./..." || sum.FiniteActive != 2 || sum.ServiceActive != 1 {
		t.Fatalf("unexpected summary = %+v", sum)
	}
	all := sum.AllBackgroundWork()
	if len(all) != 4 {
		t.Fatalf("AllBackgroundWork len = %d, want 4: %+v", len(all), all)
	}
	// Ordered: active finite first (sub-1, bg-1), then active service (bg-2), then finished (bg-3).
	if all[0].ID != "sub-1" || all[1].ID != "bg-1" || all[2].ID != "bg-2" || all[3].ID != "bg-3" {
		t.Errorf("unexpected order: %+v", all)
	}
	// Active elapsed uses nowMs; finished elapsed freezes at EndedMs - SinceMs.
	if got := all[1].ElapsedMs(20000); got != 10000 {
		t.Errorf("active elapsed = %d, want 10000", got)
	}
	if got := all[3].ElapsedMs(20000); got != 5000 {
		t.Errorf("finished elapsed = %d, want 5000", got)
	}
}

func TestFetchHarnesses(t *testing.T) {
	// Ensure FetchHarnesses never shells out to a subprocess.
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("FetchHarnesses should not exec %s %v", name, args)
		return nil, errors.New("unexpected exec")
	})

	home := t.TempDir()
	mrDir := filepath.Join(home, ".harness")
	if err := os.MkdirAll(mrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `{
		"version": 1,
		"generatedAt": 1795182582,
		"agents": [
			{
				"name": "antigravity",
				"binary": "/usr/local/bin/agy",
				"binaryState": "ok",
				"note": "Antigravity CLI against the router",
				"profiles": [
					{"name": "work", "displayName": "Work Profile", "source": "store", "agent": "antigravity"}
				]
			},
			{
				"name": "antigravity-acp",
				"binary": "",
				"binaryState": "missing",
				"note": "Missing binary without profiles",
				"profiles": []
			},
			{
				"name": "antigravity-native",
				"binary": "",
				"binaryState": "missing",
				"note": "Profile-only agent",
				"profiles": [
					{"name": "native1", "displayName": "native1", "source": "store", "agent": "antigravity-native"}
				]
			},
			{
				"name": "pi",
				"binary": "pi",
				"binaryState": "ok",
				"note": "Pi, plain exec",
				"profiles": null
			}
		]
	}`
	if err := os.WriteFile(filepath.Join(mrDir, "harnesses.json"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TMUX_SPACES_HARNESSES_FILE", "")
	t.Setenv("HOME", home)

	file, err := FetchHarnesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if file.Version != 1 || len(file.Agents) != 3 {
		t.Fatalf("got version=%d agents=%+v", file.Version, file.Agents)
	}
	if file.Agents[0].Name != "antigravity" || file.Agents[0].Description != "Antigravity CLI against the router" {
		t.Errorf("agent[0] = %+v", file.Agents[0])
	}
	if len(file.Agents[0].Profiles) != 1 || file.Agents[0].Profiles[0].DisplayName != "Work Profile" {
		t.Errorf("profiles = %+v", file.Agents[0].Profiles)
	}
	// Missing binary with profiles is kept; missing binary without profiles is filtered out.
	if file.Agents[1].Name != "antigravity-native" {
		t.Errorf("agent[1] = %+v, want antigravity-native", file.Agents[1])
	}
	if file.Agents[2].Name != "pi" || len(file.Agents[2].Profiles) != 0 {
		t.Errorf("agent[2] = %+v", file.Agents[2])
	}

	// Shadow profile HOME under ~/.harness/profiles/.../home falls back to real ~/.harness/harnesses.json.
	shadowHome := filepath.Join(mrDir, "profiles", "antigravity", "work", "home")
	if err := os.MkdirAll(shadowHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", shadowHome)
	file, err = FetchHarnesses(context.Background())
	if err != nil {
		t.Fatalf("shadow home fallback failed: %v", err)
	}
	if len(file.Agents) != 3 {
		t.Fatalf("shadow home agents = %+v", file.Agents)
	}

	// Explicit override and legacy snake_case schema compatibility.
	legacyPath := filepath.Join(t.TempDir(), "legacy.json")
	legacyPayload := `{"schema_version":2,"agents":[` +
		`{"name":"codex","description":"Codex","profiles":[{"name":"router","display_name":"Router","source":"t3"}]},` +
		`{"name":"vibe","profiles":null}]}`
	if err := os.WriteFile(legacyPath, []byte(legacyPayload), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_SPACES_HARNESSES_FILE", legacyPath)
	file, err = FetchHarnesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Agents) != 2 || file.Agents[0].Description != "Codex" || file.Agents[0].Profiles[0].DisplayName != "Router" {
		t.Errorf("legacy = %+v", file.Agents)
	}

	// SPACES_HARNESSES_PATH also overrides the harnesses file path.
	t.Setenv("TMUX_SPACES_HARNESSES_FILE", "")
	t.Setenv("SPACES_HARNESSES_PATH", legacyPath)
	file, err = FetchHarnesses(context.Background())
	if err != nil || len(file.Agents) != 2 {
		t.Fatalf("SPACES_HARNESSES_PATH = (%+v, %v)", file, err)
	}

	// When no harnesses.json exists on disk, FetchHarnesses builds directly in-process.
	emptyHome := t.TempDir()
	t.Setenv("SPACES_HARNESSES_PATH", "")
	t.Setenv("HOME", emptyHome)
	file, err = FetchHarnesses(context.Background())
	if err != nil {
		t.Fatalf("in-process FetchHarnesses error: %v", err)
	}
	if file.Version != 1 {
		t.Errorf("in-process version = %d, want 1", file.Version)
	}
}
