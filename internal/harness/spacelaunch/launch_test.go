package spacelaunch

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/profiles"
)

// fakeZellij installs a $ZELLIJ_BIN shell script driven by files in dir:
// sessions lists running sessions, clients lists attached clients, argv.log
// records invocations. Mirrors the zjcli test harness.
func fakeZellij(t *testing.T, dir string) {
	t.Helper()
	script := filepath.Join(dir, "zellij")
	body := "#!/bin/sh\n" +
		"log=\"" + dir + "/argv.log\"\n" +
		"printf '%s\\n' \"$*\" >> \"$log\"\n" +
		"if [ \"$1\" = list-sessions ]; then cat \"" + dir + "/sessions\" 2>/dev/null; exit 0; fi\n" +
		"if [ \"$1\" = --session ]; then\n" +
		"  if [ \"$3 $4\" = \"action list-clients\" ]; then cat \"" + dir + "/clients\" 2>/dev/null; exit 0; fi\n" +
		"  if [ \"$3 $4\" = \"action new-tab\" ]; then exit 0; fi\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZELLIJ_BIN", script)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func argvLog(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "argv.log"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func fakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func silence(t *testing.T) {
	t.Helper()
	oldOut, oldErr := stdout, stderr
	stdout, stderr = io.Discard, io.Discard
	t.Cleanup(func() { stdout, stderr = oldOut, oldErr })
}

func TestRunDispatch(t *testing.T) {
	testPaths(t)
	silence(t)
	if code := Run(nil); code != ExitError {
		t.Errorf("exit = %d, want usage error", code)
	}
	if code := Run([]string{"bogus"}); code != ExitError {
		t.Errorf("exit = %d, want unknown-command error", code)
	}
}

func TestSpaceLaunchAgentSessionValidation(t *testing.T) {
	testPaths(t)
	silence(t)
	dir := t.TempDir()
	fakeZellij(t, dir)
	writeFile(t, dir, "sessions", "s\n")
	writeFile(t, dir, "clients", "HEADER\npane_1 x\n")

	if code := Run([]string{"__space-launch-agent"}); code != ExitError {
		t.Errorf("exit = %d, want usage error", code)
	}
	if code := Run([]string{"__space-launch-agent", "only-session"}); code != ExitError {
		t.Errorf("exit = %d, want usage error", code)
	}
	if code := Run([]string{"__space-launch-agent", "gone", "opencode"}); code != ExitError {
		t.Errorf("exit = %d, want session error", code)
	}
}

func headlessAccess(t *testing.T) {
	t.Helper()
	old := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = old })
	t.Setenv("HARNESS_ACCESS", "")
	t.Setenv("HARNESS_AGENT", "")
}

func TestSpaceLaunchAgentRawNewTab(t *testing.T) {
	paths := testPaths(t)
	headlessAccess(t)
	homeBin := filepath.Join(paths.Home, ".local", "bin")
	if err := os.MkdirAll(homeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin(t, homeBin, "codex")
	t.Setenv("PATH", homeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	fakeZellij(t, dir)
	writeFile(t, dir, "sessions", "s\n")
	writeFile(t, dir, "clients", "HEADER\npane_1 x\n")

	if code := Run([]string{"__space-launch-agent", "s", "codex", "--model", "x"}); code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	log := strings.Join(argvLog(t, dir), "\n")
	if !strings.Contains(log, "--session s action new-tab --layout ") {
		t.Errorf("no session new-tab in log:\n%s", log)
	}
	if strings.Contains(log, "attach") {
		t.Errorf("background launch must not attach, log:\n%s", log)
	}
}

func TestWriteAgentLayoutArgs(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	path, code := writeAgentLayout("codex", "/tmp", []string{"codex", "profile", "codex_router", "--model", "x"})
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one args line carrying every argument: Zellij ignores
	// repeated args lines, which used to drop the profile.
	lines := strings.Split(string(raw), "\n")
	argsLines := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "args ") {
			argsLines++
			for _, want := range []string{`"codex"`, `"profile"`, `"codex_router"`, `"--model"`, `"x"`} {
				if !strings.Contains(line, want) {
					t.Errorf("args line missing %s: %s", want, line)
				}
			}
		}
	}
	if argsLines != 1 {
		t.Errorf("want exactly 1 args line, got %d:\n%s", argsLines, string(raw))
	}
}

func TestSpaceLaunchAgentProfile(t *testing.T) {
	paths := testPaths(t)
	headlessAccess(t)
	silence(t)
	dir := t.TempDir()
	fakeZellij(t, dir)
	writeFile(t, dir, "sessions", "s\n")
	writeFile(t, dir, "clients", "HEADER\npane_1 x\n")
	binDir := t.TempDir()
	homeBin := filepath.Join(paths.Home, ".local", "bin")
	if err := os.MkdirAll(homeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin(t, homeBin, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	store, err := profiles.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	prof := profiles.Profile{Agent: "codex", DisplayName: "Codex Router", Enabled: true, Router: true}
	if err := store.Create("codex_router", prof, profiles.CreateOptions{NoSeed: true}); err != nil {
		t.Fatal(err)
	}

	prof.Agent = "claude"
	if err := store.Create("codex_router", prof, profiles.CreateOptions{NoSeed: true}); err != nil {
		t.Fatal(err)
	}

	if code := Run([]string{"__space-launch-agent", "s", "codex", "--profile", "codex_router"}); code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	log := strings.Join(argvLog(t, dir), "\n")
	if !strings.Contains(log, "--session s action new-tab --layout ") {
		t.Fatalf("no session new-tab in log:\n%s", log)
	}
	layoutPath := strings.TrimSpace(strings.SplitN(log, "--layout ", 2)[1])
	layout, err := os.ReadFile(layoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(layout), `"codex" "profile" "codex_router"`) {
		t.Errorf("pane layout omitted the selected agent/profile:\n%s", layout)
	}
	if code := Run([]string{"__space-launch-agent", "s", "codex", "--profile", "nope"}); code != ExitError {
		t.Errorf("exit = %d, want unknown-profile error", code)
	}
}
