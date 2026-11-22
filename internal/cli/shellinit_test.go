package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/agent"
)

func TestShellInit_SupportedShellsAndDynamicPlugins(t *testing.T) {
	wantAgents := []string{
		"claude",
		"codex",
		"agy",
		"cursor-agent",
		"muse",
		"kimi",
		"kilo",
		"opencode",
		"deepseek",
	}

	for _, sh := range []string{"zsh", "bash", "fish"} {
		t.Run(sh, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			env := Env{Stdout: &out, Stderr: &errBuf}
			if code := Run([]string{"shell-init", sh}, env); code != ExitOK {
				t.Fatalf("shell-init %s exit = %d, stderr = %q", sh, code, errBuf.String())
			}
			script := out.String()
			if script == "" {
				t.Fatalf("shell-init %s produced empty script", sh)
			}
			for _, name := range wantAgents {
				if !strings.Contains(script, name) {
					t.Errorf("shell-init %s missing registered agent %q", sh, name)
				}
			}
			if !strings.Contains(script, "spaces __space-hooks --record-outcome") {
				t.Errorf("shell-init %s missing spaces __space-hooks --record-outcome invocation", sh)
			}
			if !strings.Contains(script, "TMUX_PANE") {
				t.Errorf("shell-init %s missing TMUX_PANE guard", sh)
			}
			// WorktreeOnly plugins must not be included in runtime matchers.
			if strings.Contains(script, "windsurf") {
				t.Errorf("shell-init %s unexpectedly contains WorktreeOnly plugin windsurf", sh)
			}
		})
	}
}

func TestShellInit_HelpAndErrors(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"help flag", []string{"shell-init", "--help"}, ExitOK},
		{"short help", []string{"shell-init", "-h"}, ExitOK},
		{"help subcommand", []string{"help", "shell-init"}, ExitOK},
		{"missing shell", []string{"shell-init"}, ExitUsage},
		{"unknown shell", []string{"shell-init", "csh"}, ExitUsage},
		{"too many args", []string{"shell-init", "zsh", "bash"}, ExitUsage},
		{"unknown flag", []string{"shell-init", "--bogus", "zsh"}, ExitUsage},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			env := Env{Stdout: &out, Stderr: &errBuf}
			got := Run(tc.args, env)
			if got != tc.wantCode {
				t.Fatalf("Run(%v) = %d, want %d (stdout=%q stderr=%q)", tc.args, got, tc.wantCode, out.String(), errBuf.String())
			}
		})
	}
}

func TestRuntimeAgentCommandNames_DynamicRegistry(t *testing.T) {
	reg := agent.NewRegistry(
		&agent.BasePlugin{PluginSpec: agent.Spec{
			ID:      "custom-agent",
			Binary:  "custom-bin",
			Aliases: []string{"custom-alias", "has space", "bad|meta"},
		}},
		&agent.BasePlugin{PluginSpec: agent.Spec{
			ID:           "wt-only",
			WorktreeOnly: true,
		}},
	)
	got := runtimeAgentCommandNames(reg)
	want := []string{"custom-agent", "custom-alias", "custom-bin"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("runtimeAgentCommandNames() = %v, want %v", got, want)
	}
}

func TestShellInit_SyntaxCheck(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			if code := Run([]string{"shell-init", sh}, Env{Stdout: &out, Stderr: &errBuf}); code != ExitOK {
				t.Fatalf("shell-init %s exit = %d, stderr = %q", sh, code, errBuf.String())
			}
			cmd := exec.Command(bin, "-n")
			cmd.Stdin = strings.NewReader(out.String())
			if res, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s -n failed: %v\noutput: %s", sh, err, string(res))
			}
		})
	}
}

type shellOutcomePayload struct {
	Pane    string `json:"pane"`
	App     string `json:"app"`
	Profile string `json:"profile"`
	Outcome string `json:"outcome"`
	Text    string `json:"text"`
	CWD     string `json:"cwd"`
	TsMs    int64  `json:"ts_ms"`
}

func TestShellInit_RuntimeBehavior(t *testing.T) {
	for _, sh := range []string{"bash", "zsh"} {
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			if code := Run([]string{"shell-init", sh}, Env{Stdout: &out, Stderr: &errBuf}); code != ExitOK {
				t.Fatalf("shell-init %s exit = %d, stderr = %q", sh, code, errBuf.String())
			}
			tmp := t.TempDir()
			logFile := filepath.Join(tmp, "outcomes.jsonl")
			stubSpaces := filepath.Join(tmp, "spaces")
			stubBody := "#!/bin/sh\nif [ \"$1\" = \"__space-hooks\" ] && [ \"$2\" = \"--record-outcome\" ]; then\n  printf '%s\\n' \"$3\" >> " + logFile + "\nfi\n"
			if err := os.WriteFile(stubSpaces, []byte(stubBody), 0o755); err != nil {
				t.Fatalf("write stub spaces: %v", err)
			}

			// Driver sources the generated hook, exercises direct launch, profiled
			// harness launch, non-zero exit, ignored commands, empty TMUX_PANE, and
			// empty-line precmd re-entry.
			driver := out.String() + `
export PATH="` + tmp + `:$PATH"
export TMUX_PANE="%7"

# 1. Direct agent launch succeeding (exit 0)
_tmux_spaces_preexec "claude -p 'hello'"
(exit 0); _tmux_spaces_precmd

# 2. Empty line / second precmd must NOT re-fire
(exit 0); _tmux_spaces_precmd

# 3. Profiled harness/spaces launches failing (exit 42 / exit 0)
_tmux_spaces_preexec "FOO=1 harness agy profile saucerf166 --resume abc"
(exit 42); _tmux_spaces_precmd
_tmux_spaces_preexec "spaces claude profile work --resume xyz"
(exit 0); _tmux_spaces_precmd
_tmux_spaces_preexec "tmux-spaces codex profile fast"
(exit 0); _tmux_spaces_precmd

# 4. Ignored harness/spaces subcommands & non-agent commands
_tmux_spaces_preexec "harness router status"
(exit 0); _tmux_spaces_precmd
_tmux_spaces_preexec "spaces profile list"
(exit 0); _tmux_spaces_precmd
_tmux_spaces_preexec "git status"
(exit 0); _tmux_spaces_precmd

# 5. No-op when TMUX_PANE is unset
unset TMUX_PANE
_tmux_spaces_preexec "codex"
(exit 0); _tmux_spaces_precmd

# Wait briefly for backgrounded subshells to flush.
sleep 0.2
`
			var cmd *exec.Cmd
			if sh == "zsh" {
				cmd = exec.Command(bin, "-f", "-c", driver)
			} else {
				cmd = exec.Command(bin, "--norc", "--noprofile", "-c", driver)
			}
			cmd.Dir = tmp
			if res, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s driver failed: %v\noutput: %s", sh, err, string(res))
			}

			raw, err := os.ReadFile(logFile)
			if err != nil {
				t.Fatalf("read outcome log: %v", err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != 4 {
				t.Fatalf("expected 4 recorded outcomes, got %d:\n%s", len(lines), string(raw))
			}

			var p1, p2, p3, p4 shellOutcomePayload
			if err := json.Unmarshal([]byte(lines[0]), &p1); err != nil {
				t.Fatalf("unmarshal outcome 1 (%q): %v", lines[0], err)
			}
			if err := json.Unmarshal([]byte(lines[1]), &p2); err != nil {
				t.Fatalf("unmarshal outcome 2 (%q): %v", lines[1], err)
			}
			if err := json.Unmarshal([]byte(lines[2]), &p3); err != nil {
				t.Fatalf("unmarshal outcome 3 (%q): %v", lines[2], err)
			}
			if err := json.Unmarshal([]byte(lines[3]), &p4); err != nil {
				t.Fatalf("unmarshal outcome 4 (%q): %v", lines[3], err)
			}

			if p1.Pane != "%7" || p1.App != "claude" || p1.Profile != "" || p1.Outcome != "done" || p1.Text != "finished" || p1.TsMs <= 0 {
				t.Errorf("unexpected outcome 1: %+v", p1)
			}
			if p2.Pane != "%7" || p2.App != "agy" || p2.Profile != "saucerf166" || p2.Outcome != "failed" || p2.Text != "exited with status 42" || p2.TsMs <= 0 {
				t.Errorf("unexpected outcome 2: %+v", p2)
			}
			if p3.Pane != "%7" || p3.App != "claude" || p3.Profile != "work" || p3.Outcome != "done" || p3.TsMs <= 0 {
				t.Errorf("unexpected outcome 3: %+v", p3)
			}
			if p4.Pane != "%7" || p4.App != "codex" || p4.Profile != "fast" || p4.Outcome != "done" || p4.TsMs <= 0 {
				t.Errorf("unexpected outcome 4: %+v", p4)
			}
		})
	}
}
