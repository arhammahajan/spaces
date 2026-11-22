package osc

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Wrap runs the agent as a child process and reports its attention
// lifecycle: Begin registers the pane, the child runs with inherited
// stdio so it owns the tty and receives terminal signals naturally
// (same process group — Ctrl-C reaches it directly), and Finish
// reports the terminal state once the child exits. Wrap returns the
// child's exit code; a spawn failure maps to 127, shell-style.
func Wrap(app, binary string, argv, envAppend []string) int {
	Begin(app)
	code := runChild(binary, argv, envAppend)
	Finish(app, code)
	return code
}

// WrapWithProfile runs the agent as a child process like Wrap, and also
// projects the harness profile name onto `@spaces_profile` in tmux (and onto
// the OSC 7501 records) for the lifetime of the run, unsetting `@spaces_profile`
// once the child exits.
func WrapWithProfile(app, profile, binary string, argv, envAppend []string) int {
	SetTmuxProfile(profile)
	BeginWithProfile(app, profile)
	code := runChild(binary, argv, envAppend)
	FinishWithProfile(app, profile, code)
	SetTmuxProfile("")
	return code
}

// runChild starts binary with argv, the current environment plus
// envAppend and inherited stdio, waits for it, and maps the outcome
// to an exit code.
func runChild(binary string, argv, envAppend []string) int {
	cmd := exec.Command(binary, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = childEnv(envAppend)
	// Same process group: terminal-generated signals (Ctrl-C, Ctrl-\)
	// are delivered to the child directly, exactly as if the user had
	// launched the agent from the shell.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: false}
	return waitCode(cmd)
}

// waitCode runs cmd to completion and maps the result to a
// shell-style exit code: the child's recorded status, 128+signal when
// the child died to a signal, 127 when it could not start.
func waitCode(cmd *exec.Cmd) int {
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		code := ee.ExitCode()
		if code < 0 {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
		}
		return code
	}
	return 127
}

// childEnv returns the current environment plus envAppend, with
// envAppend keys winning: inherited entries they shadow are dropped,
// because an exec environment matches on the first occurrence of a
// key.
func childEnv(envAppend []string) []string {
	if len(envAppend) == 0 {
		return os.Environ()
	}
	shadowed := make(map[string]bool, len(envAppend))
	for _, kv := range envAppend {
		if k, _, ok := strings.Cut(kv, "="); ok {
			shadowed[k] = true
		}
	}
	env := os.Environ()
	merged := make([]string, 0, len(env)+len(envAppend))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && shadowed[k] {
			continue
		}
		merged = append(merged, kv)
	}
	return append(merged, envAppend...)
}
