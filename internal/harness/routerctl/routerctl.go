// Package routerctl queries the model-router daemon over HTTP and validates
// its configuration.
//
// Every home- or environment-derived location comes from harnessenv.Paths,
// and every process or systemctl invocation goes through the injectable
// hooks on Options (Exec, Kill, LookPath), so tests run with fakes and never
// touch the real home, systemctl, or router binaries.
package routerctl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// DefaultUnit is the systemd user unit managed in systemd mode.
const DefaultUnit = "model-router.service"

// DefaultHTTPTimeout applies to all router HTTP queries when
// Options.HTTPTimeout is zero.
const DefaultHTTPTimeout = 5 * time.Second

// Options configures router queries and offline config validation. The first
// block is the contract surface; the second block holds injectable hooks. A
// nil hook selects the real implementation, and tests inject fakes.
type Options struct {
	Paths        harnessenv.Paths
	Router       harnessenv.RouterSettings // URL + key for queries
	Unit         string                    // systemd user unit, default DefaultUnit
	Systemctl    string                    // binary path, default "systemctl"; tests inject fakes
	RouterBin    string                    // direct-mode binary; Detect fills it
	RouterConfig string                    // --config for direct mode; Detect fills it
	PidPath      string                    // direct-mode pidfile; default $StateHome/harness/router.pid
	HTTPTimeout  time.Duration             // default DefaultHTTPTimeout

	// Exec runs a command to completion and returns its combined output.
	// Used for systemctl invocations.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Kill delivers sig to pid. The default is syscall.Kill.
	Kill func(pid int, sig syscall.Signal) error
	// LookPath resolves a binary from PATH. The default is exec.LookPath.
	LookPath func(file string) (string, error)
}

func (o Options) unit() string {
	if o.Unit != "" {
		return o.Unit
	}
	return DefaultUnit
}

func (o Options) systemctl() string {
	if o.Systemctl != "" {
		return o.Systemctl
	}
	return "systemctl"
}

func (o Options) httpTimeout() time.Duration {
	if o.HTTPTimeout > 0 {
		return o.HTTPTimeout
	}
	return DefaultHTTPTimeout
}

func (o Options) execFn() func(ctx context.Context, name string, args ...string) ([]byte, error) {
	if o.Exec != nil {
		return o.Exec
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		return buf.Bytes(), err
	}
}

func (o Options) lookPathFn() func(string) (string, error) {
	if o.LookPath != nil {
		return o.LookPath
	}
	return exec.LookPath
}

func (o Options) killFn() func(int, syscall.Signal) error {
	if o.Kill != nil {
		return o.Kill
	}
	return syscall.Kill
}

// Detect builds Options from paths and router settings, resolving the
// direct-mode binary and config with the contract search orders:
//
// RouterBin: $HARNESS_ROUTER_BIN, then the newest
// $Home/.local/libexec/model-router/model-router-go*, then
// $Home/dev/model-router/model-router-go, then "model-router-go" from PATH.
//
// RouterConfig: $HARNESS_ROUTER_CONFIG, then
// $ConfigHome/model-router/router-go.toml, then the literal "router.toml".
//
// PidPath defaults to $StateHome/harness/router.pid;
// HTTPTimeout defaults to DefaultHTTPTimeout.
func Detect(paths harnessenv.Paths, router harnessenv.RouterSettings) Options {
	opts := Options{
		Paths:       paths,
		Router:      router,
		Unit:        DefaultUnit,
		PidPath:     filepath.Join(paths.StateHome, "harness", "router.pid"),
		HTTPTimeout: DefaultHTTPTimeout,
	}
	if sys, err := exec.LookPath("systemctl"); err == nil && sys != "" {
		opts.Systemctl = sys
	} else {
		opts.Systemctl = "systemctl"
	}
	if bin := os.Getenv("HARNESS_ROUTER_BIN"); bin != "" {
		opts.RouterBin = bin
	} else if newest, ok := newestLibexecBin(paths); ok {
		opts.RouterBin = newest
	} else if dev := filepath.Join(paths.Home, "dev", "model-router", "model-router-go"); fileExists(dev) {
		opts.RouterBin = dev
	} else if p, err := exec.LookPath("model-router-go"); err == nil && p != "" {
		opts.RouterBin = p
	} else {
		opts.RouterBin = "model-router-go"
	}
	if cfg := os.Getenv("HARNESS_ROUTER_CONFIG"); cfg != "" {
		opts.RouterConfig = cfg
	} else if cand := filepath.Join(paths.ConfigHome, "model-router", "router-go.toml"); fileExists(cand) {
		opts.RouterConfig = cand
	} else {
		opts.RouterConfig = "router.toml"
	}
	return opts
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// newestLibexecBin returns the most recently modified
// $Home/.local/libexec/model-router/model-router-go* entry. Ties break by
// greatest base name so the choice is deterministic.
func newestLibexecBin(paths harnessenv.Paths) (string, bool) {
	matches, err := filepath.Glob(filepath.Join(paths.Home, ".local", "libexec", "model-router", "model-router-go*"))
	if err != nil || len(matches) == 0 {
		return "", false
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var cands []candidate
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil || st.IsDir() {
			continue
		}
		cands = append(cands, candidate{path: m, modTime: st.ModTime()})
	}
	if len(cands) == 0 {
		return "", false
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].modTime.Equal(cands[j].modTime) {
			return cands[i].path > cands[j].path
		}
		return cands[i].modTime.After(cands[j].modTime)
	})
	return cands[0].path, true
}

// systemdManaged reports whether the user unit exists: systemctl --user
// list-unit-files names it, or systemctl --user is-enabled succeeds.
func systemdManaged(ctx context.Context, opts Options) bool {
	sys := opts.systemctl()
	execFn := opts.execFn()
	unit := opts.unit()
	if out, err := execFn(ctx, sys, "--user", "list-unit-files", unit); err == nil && strings.Contains(string(out), unit) {
		return true
	}
	if _, err := execFn(ctx, sys, "--user", "is-enabled", unit); err == nil {
		return true
	}
	return false
}

func readPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil || pid <= 0 {
		return 0, fmt.Errorf("unparsable pidfile %s", path)
	}
	return pid, nil
}

// pidAlive reports whether pid refers to a live process. EPERM (no
// permission to signal) still counts as alive; ESRCH means gone.
func pidAlive(kill func(int, syscall.Signal) error, pid int) bool {
	if err := kill(pid, syscall.Signal(0)); err == nil {
		return true
	} else {
		return !isNotExist(err)
	}
}

func isNotExist(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "no such process")
}
