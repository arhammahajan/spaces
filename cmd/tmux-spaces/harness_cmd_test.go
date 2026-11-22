package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/profiles"
)

type capturedHarnessLaunch struct {
	calls    int
	agent    string
	profile  string
	resolved agents.Resolved
}

func isolateHarnessEnv(t *testing.T) harnessenv.Paths {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("HARNESS_DOTFILES", filepath.Join(tmp, "dots-missing"))
	t.Setenv("HARNESS_AGENT", "")
	t.Setenv("HARNESS_ACCESS", "default")
	return harnessenv.DefaultPaths()
}

func stubHarnessLaunch(t *testing.T) *capturedHarnessLaunch {
	t.Helper()
	cap := &capturedHarnessLaunch{}
	orig := harnessLaunchAgent
	t.Cleanup(func() { harnessLaunchAgent = orig })
	harnessLaunchAgent = func(resolved agents.Resolved, agent, profile string) int {
		cap.calls++
		cap.agent = agent
		cap.profile = profile
		cap.resolved = resolved
		return 0
	}
	return cap
}

func mustCreateHarnessProfile(t *testing.T, paths harnessenv.Paths, name, agent string, enabled bool, prepend ...string) {
	t.Helper()
	s, err := profiles.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	p := profiles.Profile{Agent: agent, Enabled: enabled, Router: true, PrependArgs: prepend}
	if err := s.Create(name, p, profiles.CreateOptions{NoSeed: true}); err != nil {
		t.Fatalf("Create(%s) = %v", name, err)
	}
}

func writeHarnessT3(t *testing.T, paths harnessenv.Paths, instances string) {
	t.Helper()
	dir := filepath.Join(paths.Home, ".t3", "userdata")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"providerInstances": {` + instances + `}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func captureStderr(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	code := fn()
	_ = w.Close()
	os.Stderr = orig
	data, _ := io.ReadAll(r)
	return code, string(data)
}

func captureStdout(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	code := fn()
	_ = w.Close()
	os.Stdout = orig
	data, _ := io.ReadAll(r)
	return code, string(data)
}

func captureBoth(t *testing.T, fn func() int) (int, string, string) {
	t.Helper()
	outOrig, errOrig := os.Stdout, os.Stderr
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	os.Stdout, os.Stderr = ow, ew
	code := fn()
	_ = ow.Close()
	_ = ew.Close()
	os.Stdout, os.Stderr = outOrig, errOrig
	out, _ := io.ReadAll(or)
	errs, _ := io.ReadAll(er)
	return code, string(out), string(errs)
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func mustCreateCLIProfile(t *testing.T, name, agent string) {
	t.Helper()
	isolateHarnessEnv(t)
	if code, _, _ := captureBoth(t, func() int {
		return runHarnessProfile([]string{"create", name, "--agent", agent})
	}); code != 0 {
		t.Fatalf("create %s = %d", name, code)
	}
}

func TestHarnessBareAgentZeroProfiles(t *testing.T) {
	isolateHarnessEnv(t)
	cap := stubHarnessLaunch(t)
	code, _ := captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"pi", "--hello"}) })
	if code != 0 {
		t.Fatalf("bare launch exit = %d, want 0", code)
	}
	if cap.calls != 1 || cap.agent != "pi" || cap.profile != "default" {
		t.Fatalf("launch = %+v, want one pi default launch", cap)
	}
	if !containsArg(cap.resolved.Args, "--hello") {
		t.Fatalf("forwarded args missing --hello: %v", cap.resolved.Args)
	}
	if cap.resolved.Binary != "pi" {
		t.Fatalf("binary = %q, want pi", cap.resolved.Binary)
	}
}

func TestHarnessSecondProfileRequiresExplicitSelection(t *testing.T) {
	paths := isolateHarnessEnv(t)
	mustCreateHarnessProfile(t, paths, "solo", "pi", true, "--pre")
	cap := stubHarnessLaunch(t)
	code, msg := captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"pi", "--foo", "bar"}) })
	if code != 2 {
		t.Fatalf("2-profile exit = %d, want 2 (stderr %q)", code, msg)
	}
	if cap.calls != 0 {
		t.Fatal("2-profile setup must not auto-launch")
	}
	for _, want := range []string{"default", "solo", "spaces pi profile"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr %q must mention %q", msg, want)
		}
	}
}

func TestHarnessExplicitDefaultProfileLaunchesUnmodified(t *testing.T) {
	paths := isolateHarnessEnv(t)
	mustCreateHarnessProfile(t, paths, "solo", "pi", true, "--pre")
	cap := stubHarnessLaunch(t)
	code, msg := captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"pi", "profile", "default", "--foo"}) })
	if code != 0 {
		t.Fatalf("explicit default exit = %d, want 0 (%q)", code, msg)
	}
	if cap.calls != 1 || cap.agent != "pi" {
		t.Fatalf("launch = %+v, want one pi default launch", cap)
	}
	if containsArg(cap.resolved.Args, "--pre") {
		t.Fatalf("default profile must be unmodified, got %v", cap.resolved.Args)
	}
	if !containsArg(cap.resolved.Args, "--foo") {
		t.Fatalf("default profile must forward caller args, got %v", cap.resolved.Args)
	}
}

func TestHarnessMultiProfileFailsWithHint(t *testing.T) {
	paths := isolateHarnessEnv(t)
	mustCreateHarnessProfile(t, paths, "alpha", "pi", true)
	mustCreateHarnessProfile(t, paths, "beta", "pi", true)
	cap := stubHarnessLaunch(t)
	code, msg := captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"pi", "--foo"}) })
	if code != 2 {
		t.Fatalf("multi-profile exit = %d, want 2 (stderr %q)", code, msg)
	}
	if cap.calls != 0 {
		t.Fatal("multi-profile must not launch anything")
	}
	for _, want := range []string{"alpha", "beta", "spaces pi profile"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr %q must mention %q", msg, want)
		}
	}
}

func TestHarnessExplicitProfileLaunch(t *testing.T) {
	paths := isolateHarnessEnv(t)
	mustCreateHarnessProfile(t, paths, "alpha", "pi", true)
	mustCreateHarnessProfile(t, paths, "beta", "pi", true)
	cap := stubHarnessLaunch(t)
	code, _ := captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"pi", "profile", "beta", "--x", "1"}) })
	if code != 0 {
		t.Fatalf("explicit launch exit = %d, want 0", code)
	}
	if cap.calls != 1 || cap.agent != "pi" || cap.profile != "beta" {
		t.Fatalf("launch = %+v, want beta profile launch", cap)
	}
	if !containsArg(cap.resolved.Args, "--x") || !containsArg(cap.resolved.Args, "1") {
		t.Fatalf("explicit caller args must forward, got %v", cap.resolved.Args)
	}
}

func TestHarnessExplicitProfileErrors(t *testing.T) {
	paths := isolateHarnessEnv(t)
	mustCreateHarnessProfile(t, paths, "alpha", "pi", true)
	mustCreateHarnessProfile(t, paths, "c-one", "codex", true)
	cap := stubHarnessLaunch(t)

	code, msg := captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"pi", "profile"}) })
	if code != 2 || !strings.Contains(msg, "alpha") {
		t.Fatalf("missing name = (%d, %q)", code, msg)
	}
	code, msg = captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"pi", "profile", "nope"}) })
	if code != 2 || !strings.Contains(msg, "nope") || !strings.Contains(msg, "alpha") {
		t.Fatalf("unknown profile = (%d, %q)", code, msg)
	}
	code, msg = captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"codex", "profile", "alpha"}) })
	if code != 2 || !strings.Contains(msg, "alpha") || !strings.Contains(msg, "pi") || !strings.Contains(msg, "codex") {
		t.Fatalf("wrong agent = (%d, %q)", code, msg)
	}
	if cap.calls != 0 {
		t.Fatalf("expected 0 launches on errors, got %d", cap.calls)
	}
}

func TestHarnessAgentAliasAndLaunchSubcommand(t *testing.T) {
	paths := isolateHarnessEnv(t)
	mustCreateHarnessProfile(t, paths, "a-one", "antigravity", true)
	cap := stubHarnessLaunch(t)

	code, handled := maybeRunHarnessCommand("spaces", []string{"agy", "profile", "a-one", "--z"})
	if !handled || code != 0 || cap.calls != 1 || cap.agent != "antigravity" || !containsArg(cap.resolved.Args, "--z") {
		t.Fatalf("direct alias dispatch = (handled=%v, code=%d, cap=%+v)", handled, code, cap)
	}

	code, handled = maybeRunHarnessCommand("spaces", []string{"launch", "agy", "profile", "a-one", "--w"})
	if !handled || code != 0 || cap.calls != 2 || cap.agent != "antigravity" || !containsArg(cap.resolved.Args, "--w") {
		t.Fatalf("launch subcommand dispatch = (handled=%v, code=%d, cap=%+v)", handled, code, cap)
	}
}

func TestHarnessAgentEnvSupportAndNonShadowing(t *testing.T) {
	paths := isolateHarnessEnv(t)
	mustCreateHarnessProfile(t, paths, "alpha", "pi", true)
	mustCreateHarnessProfile(t, paths, "beta", "pi", true)
	t.Setenv("HARNESS_AGENT", "pi")
	cap := stubHarnessLaunch(t)

	// HARNESS_AGENT with bare args or empty args dispatches to bare agent launch.
	code, handled := maybeRunHarnessEnvAgent([]string{"probe-arg"})
	if !handled || code != 0 || cap.calls != 1 || cap.agent != "pi" || cap.profile != "" || !containsArg(cap.resolved.Args, "probe-arg") {
		t.Fatalf("HARNESS_AGENT dispatch = (handled=%v, code=%d, cap=%+v)", handled, code, cap)
	}
	code, handled = maybeRunHarnessEnvAgent(nil)
	if !handled || code != 0 || cap.calls != 2 {
		t.Fatalf("HARNESS_AGENT empty args = (handled=%v, code=%d, cap=%+v)", handled, code, cap)
	}

	// Built-in spaces subcommands are NOT shadowed even when HARNESS_AGENT is set.
	for _, sub := range []string{"status", "status-bar", "panes", "hook", "profile", "popup"} {
		if _, h := maybeRunHarnessEnvAgent([]string{sub}); h {
			t.Errorf("HARNESS_AGENT must not shadow spaces subcommand %q", sub)
		}
	}
}

func TestHarnessDispatchDoesNotShadowExistingSpacesCommands(t *testing.T) {
	isolateHarnessEnv(t)
	stubHarnessLaunch(t)

	for _, cmd := range []string{"status", "status-bar", "space", "panes", "popup", "inbox", "tabs", "worktrees", "resurrect", "agent", "help"} {
		if _, handled := maybeRunHarnessCommand("spaces", []string{cmd}); handled {
			t.Errorf("maybeRunHarnessCommand(spaces, %q) should not handle existing spaces command", cmd)
		}
	}
	// tmux-spaces agents (no flags) and tmux-spaces doctor stay wired to tmux-spaces TUI/doctor.
	if _, handled := maybeRunHarnessCommand("tmux-spaces", []string{"agents"}); handled {
		t.Errorf("tmux-spaces agents (no args) must fall through to TUI")
	}
	if _, handled := maybeRunHarnessCommand("tmux-spaces", []string{"doctor"}); handled {
		t.Errorf("tmux-spaces doctor must fall through to tmux doctor")
	}
	// spaces agents --json and tmux-spaces agents --json are handled natively.
	code, out := captureStdout(t, func() int {
		c, h := maybeRunHarnessCommand("spaces", []string{"agents", "--json"})
		if !h {
			t.Fatalf("spaces agents --json not handled")
		}
		return c
	})
	if code != 0 || !strings.Contains(out, `"claude"`) {
		t.Fatalf("spaces agents --json = (%d, %q)", code, out)
	}
}

func TestHarnessDisabledCustomProfileRestoresDefaultAutoLaunch(t *testing.T) {
	paths := isolateHarnessEnv(t)
	mustCreateHarnessProfile(t, paths, "dead", "pi", false, "--pre")
	cap := stubHarnessLaunch(t)
	code, msg := captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"pi", "--arg"}) })
	if code != 0 {
		t.Fatalf("disabled-excluded exit = %d, want 0 (%q)", code, msg)
	}
	if cap.calls != 1 || cap.agent != "pi" {
		t.Fatalf("only default enabled must auto-launch default, got %+v", cap)
	}
	if containsArg(cap.resolved.Args, "--pre") || !containsArg(cap.resolved.Args, "--arg") {
		t.Fatalf("expected unmodified default args, got %v", cap.resolved.Args)
	}
}

func TestHarnessT3SettingsIgnored(t *testing.T) {
	paths := isolateHarnessEnv(t)
	writeHarnessT3(t, paths, `"codex": {"displayName": "Codex"}, "codex_router": {"displayName": "Codex Router"}`)
	cap := stubHarnessLaunch(t)
	code, msg := captureStderr(t, func() int { return runHarnessAgentLaunch([]string{"codex", "--arg"}) })
	if code != 0 || cap.calls != 1 || cap.agent != "codex" || !containsArg(cap.resolved.Args, "--arg") {
		t.Fatalf("T3 settings launch = (%d, %q, %+v)", code, msg, cap)
	}
}

func TestHarnessProfileCreateEditShowCloneRenameRemove(t *testing.T) {
	isolateHarnessEnv(t)
	launch := stubHarnessLaunch(t)
	command := func(want int, args ...string) (string, string) {
		t.Helper()
		code, out, stderr := captureBoth(t, func() int {
			c, h := maybeRunHarnessCommand("spaces", args)
			if !h {
				t.Fatalf("%v not handled by maybeRunHarnessCommand", args)
			}
			return c
		})
		if code != want {
			t.Fatalf("%v exited %d, want %d (stdout: %q, stderr: %q)", args, code, want, out, stderr)
		}
		return out, stderr
	}

	out, _ := command(0, "profile", "list")
	for _, agent := range profiles.KnownAgents() {
		if !strings.Contains(out, "default\t"+agent+"\t") {
			t.Errorf("fresh profile list missing default for %s", agent)
		}
	}

	command(0, "profile", "create", "work", "--agent", "codex", "--binary", "/fake/codex", "--no-seed")
	command(0, "profile", "create", "work", "--agent", "claude", "--binary", "/fake/claude", "--no-seed")
	command(1, "profile", "create", "work", "--agent", "codex", "--no-seed")
	out, _ = command(0, "profile", "list")
	for _, row := range []string{"work\tclaude\n", "work\tcodex\n"} {
		if !strings.Contains(out, row) {
			t.Errorf("profile list missing %q", row)
		}
	}
	_, stderr := command(2, "profile", "work")
	for _, ref := range []string{"claude/work", "codex/work"} {
		if !strings.Contains(stderr, ref) {
			t.Errorf("ambiguous launch missing %q in %q", ref, stderr)
		}
	}
	command(0, "codex", "profile", "work")
	if launch.calls != 1 || launch.agent != "codex" || launch.resolved.Binary != "/fake/codex" {
		t.Fatalf("agent-scoped launch = %+v", launch)
	}
	command(0, "profile", "claude/work")
	if launch.calls != 2 || launch.agent != "claude" || launch.resolved.Binary != "/fake/claude" {
		t.Fatalf("qualified launch = %+v", launch)
	}

	command(0, "profile", "edit", "work", "--agent", "claude", "--display-name", "Claude Work")
	command(0, "profile", "disable", "claude/work")
	command(2, "claude", "profile", "work")
	command(0, "profile", "enable", "work", "--agent", "claude")
	command(0, "profile", "clone", "claude/work", "copy", "--no-seed")
	command(0, "profile", "rename", "copy", "renamed", "--agent", "claude")
	command(0, "profile", "remove", "claude/renamed", "--purge")
	command(2, "profile", "show", "claude/renamed")
	out, _ = command(0, "profile", "get", "work", "display-name", "--agent", "claude")
	if strings.TrimSpace(out) != "Claude Work" {
		t.Fatalf("Claude profile display name = %q", out)
	}
	out, _ = command(0, "profile", "path", "codex/work")
	if !strings.HasSuffix(strings.TrimSpace(out), "/codex/work/profile.json") {
		t.Fatalf("Codex profile path = %q", out)
	}
}

func TestHarnessProfileShowSecretsAndInit(t *testing.T) {
	paths := isolateHarnessEnv(t)
	code, out := captureStdout(t, func() int { return runHarnessProfile([]string{"init"}) })
	if code != 0 || strings.TrimSpace(out) != profiles.ProfilesDir(paths) {
		t.Fatalf("profile init = (%d, %q)", code, out)
	}
	s, err := profiles.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	p := profiles.Profile{
		Agent: "pi", Enabled: true, Router: true,
		DisplayName: "Show", BinaryPath: "/bin/pi",
		PrependArgs: []string{"--a", "--b"},
		Env:         map[string]string{"PLAIN": "hello", "API_KEY": "sekret", "myToken": "tok"},
	}
	if err := s.Create("showme", p, profiles.CreateOptions{NoSeed: true}); err != nil {
		t.Fatal(err)
	}
	_, out, _ = captureBoth(t, func() int { return runHarnessProfile([]string{"show", "showme"}) })
	if strings.Contains(out, "sekret") || strings.Contains(out, "tok") || strings.Count(out, "***") < 2 {
		t.Errorf("show must mask secrets:\n%s", out)
	}
	code, jsonOut := captureStdout(t, func() int { return runHarnessProfile([]string{"show", "showme", "--json"}) })
	if code != 0 {
		t.Fatalf("show --json exit = %d", code)
	}
	var snap profileShowSnapshot
	if err := json.Unmarshal([]byte(jsonOut), &snap); err != nil {
		t.Fatalf("decode show --json: %v", err)
	}
	if snap.EffectiveBinary != "/bin/pi" || snap.Env["API_KEY"] != "***" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}
