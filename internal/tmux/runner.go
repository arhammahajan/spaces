// Package tmux is a thin, testable client over the tmux CLI.
//
// All tmux invocations go through the Runner seam so unit tests can run
// without a real tmux server: production code uses ExecRunner while tests
// inject fake Runners or point TMUX_BIN at temp-dir stub scripts.
package tmux

import (
	"bytes"
	"context"
	"os"
	"os/exec"
)

// Runner executes a tmux command.
//
// args are the tmux subcommand and its flags (the binary itself is supplied
// by the implementation). err is non-nil when the process fails to start or
// exits non-zero; exitCode is the process exit status, or -1 when the process
// never ran.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout, stderr string, exitCode int, err error)
}

// ExecRunner runs the real tmux binary via os/exec.
//
// The zero value is usable; the binary resolves to $TMUX_BIN, or "tmux"
// when unset, at call time.
type ExecRunner struct {
	// Bin overrides the tmux binary path. Empty means resolve from
	// $TMUX_BIN (falling back to "tmux") on every call.
	Bin string
}

// NewExecRunner returns an ExecRunner using $TMUX_BIN, or "tmux" when unset.
func NewExecRunner() ExecRunner {
	return ExecRunner{Bin: tmuxBinary()}
}

// tmuxBinary resolves the tmux binary path from the environment.
func tmuxBinary() string {
	if bin := os.Getenv("TMUX_BIN"); bin != "" {
		return bin
	}
	return "tmux"
}

// Run executes bin with args, capturing combined outputs.
func (r ExecRunner) Run(ctx context.Context, args ...string) (string, string, int, error) {
	bin := r.Bin
	if bin == "" {
		bin = tmuxBinary()
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Never inherit the caller's stdio: capture everything so popups and
	// background uses stay quiet.
	cmd.Stdin = nil
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return stdout.String(), stderr.String(), exitErr.ExitCode(), err
		}
		// Binary missing, context cancelled, etc: the process never ran.
		return stdout.String(), stderr.String(), -1, err
	}
	return stdout.String(), stderr.String(), 0, nil
}
