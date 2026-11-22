// Package routerctl controls and queries the model-router daemon.
//
// It implements the router control surface of the harness CLI:
// systemd-first start/stop/restart with a direct-spawn fallback, plus
// read-only HTTP queries (status, models, config validation, logs).
//
// Every home- or environment-derived location comes from harnessenv.Paths,
// and every process or systemctl invocation goes through the injectable
// hooks on Options (Exec, RunOut, Spawn, Kill, LookPath), so tests run with
// fakes and never touch the real home, systemctl, or router binaries.
package routerctl

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// DefaultStopGrace is the SIGTERM-to-SIGKILL grace Restart allows a
// directly-spawned router before escalating. Stop takes an explicit grace;
// Restart has no grace parameter in the contract, so it uses this.
const DefaultStopGrace = 10 * time.Second

// Options configures router daemon control and queries. The first block is
// the contract surface; the second block holds injectable hooks. A nil hook
// selects the real implementation, and tests inject fakes.
type Options struct {
	Paths        harnessenv.Paths
	Router       harnessenv.RouterSettings // URL + key for queries
	Unit         string                    // systemd user unit, default DefaultUnit
	Systemctl    string                    // binary path, default "systemctl"; tests inject fakes
	Journalctl   string                    // journalctl binary path, default "journalctl"; tests inject fakes
	RouterBin    string                    // direct-mode binary; Detect fills it
	RouterConfig string                    // --config for direct mode; Detect fills it
	LogPath      string                    // direct-mode log; default $StateHome/harness/router.log
	PidPath      string                    // direct-mode pidfile; default $StateHome/harness/router.pid
	HTTPTimeout  time.Duration             // default DefaultHTTPTimeout

	// Exec runs a command to completion and returns its combined output.
	// Used for systemctl invocations.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
	// RunOut runs a command with stdout+stderr wired to out. Used by Logs
	// for journalctl (including -f follow, which never exits on its own).
	RunOut func(ctx context.Context, out io.Writer, name string, args ...string) error
	// Spawn starts bin detached (setsid, stdio to the log file) and
	// returns the child pid without waiting for readiness.
	Spawn func(bin string, args []string, logPath string) (int, error)
	// Kill delivers sig to pid. The default is syscall.Kill.
	Kill func(pid int, sig syscall.Signal) error
	// LookPath resolves a binary from PATH. The default is exec.LookPath.
	LookPath func(file string) (string, error)
	// Stdout receives Logs output. A nil value selects os.Stdout.
	Stdout io.Writer
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

func (o Options) journalctl() string {
	if o.Journalctl != "" {
		return o.Journalctl
	}
	return "journalctl"
}

func (o Options) httpTimeout() time.Duration {
	if o.HTTPTimeout > 0 {
		return o.HTTPTimeout
	}
	return DefaultHTTPTimeout
}

func (o Options) stdout() io.Writer {
	if o.Stdout != nil {
		return o.Stdout
	}
	return os.Stdout
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

func (o Options) runOutFn() func(ctx context.Context, out io.Writer, name string, args ...string) error {
	if o.RunOut != nil {
		return o.RunOut
	}
	return func(ctx context.Context, out io.Writer, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdout = out
		cmd.Stderr = out
		return cmd.Run()
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

func (o Options) spawnFn() func(string, []string, string) (int, error) {
	if o.Spawn != nil {
		return o.Spawn
	}
	return spawnDetached
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
// Log/Pid default to $StateHome/harness/router.log and router.pid;
// HTTPTimeout defaults to DefaultHTTPTimeout.
func Detect(paths harnessenv.Paths, router harnessenv.RouterSettings) Options {
	opts := Options{
		Paths:       paths,
		Router:      router,
		Unit:        DefaultUnit,
		LogPath:     filepath.Join(paths.StateHome, "harness", "router.log"),
		PidPath:     filepath.Join(paths.StateHome, "harness", "router.pid"),
		HTTPTimeout: DefaultHTTPTimeout,
	}
	lookPath := exec.LookPath
	if opts.LookPath != nil {
		lookPath = opts.LookPath
	}
	if sys, err := lookPath("systemctl"); err == nil && sys != "" {
		opts.Systemctl = sys
	} else {
		opts.Systemctl = "systemctl"
	}
	if jr, err := lookPath("journalctl"); err == nil && jr != "" {
		opts.Journalctl = jr
	} else {
		opts.Journalctl = "journalctl"
	}
	if bin := os.Getenv("HARNESS_ROUTER_BIN"); bin != "" {
		opts.RouterBin = bin
	} else if newest, ok := newestLibexecBin(paths); ok {
		opts.RouterBin = newest
	} else if dev := filepath.Join(paths.Home, "dev", "model-router", "model-router-go"); fileExists(dev) {
		opts.RouterBin = dev
	} else if p, err := lookPath("model-router-go"); err == nil && p != "" {
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

// Start brings the router up, systemd-first. In systemd mode it starts the
// user unit; otherwise it spawns RouterBin detached (setsid, stdout+stderr
// appended to LogPath, pid recorded in PidPath). Either way it then polls
// GET /livez until 200 within waitReady.
func Start(ctx context.Context, opts Options, waitReady time.Duration) error {
	if systemdManaged(ctx, opts) {
		if out, err := opts.execFn()(ctx, opts.systemctl(), "--user", "start", opts.unit()); err != nil {
			return fmt.Errorf("start %s: %v: %s", opts.unit(), err, strings.TrimSpace(string(out)))
		}
		return waitLivez(ctx, opts, waitReady)
	}
	return startDirect(ctx, opts, waitReady)
}

func startDirect(ctx context.Context, opts Options, waitReady time.Duration) error {
	bin := opts.RouterBin
	if bin == "" {
		return fmt.Errorf("no router binary: set HARNESS_ROUTER_BIN")
	}
	args := []string{}
	if opts.RouterConfig != "" {
		args = append(args, "--config", opts.RouterConfig)
	}
	pid, err := opts.spawnFn()(bin, args, opts.LogPath)
	if err != nil {
		return fmt.Errorf("spawn %s: %w", bin, err)
	}
	if err := writePidFile(opts.PidPath, pid); err != nil {
		return err
	}
	return waitLivez(ctx, opts, waitReady)
}

// spawnDetached starts bin detached from the calling session: a setsid
// leader whose stdout and stderr append to logPath. It returns the child
// pid without waiting.
func spawnDetached(bin string, args []string, logPath string) (int, error) {
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return 0, fmt.Errorf("create log dir: %w", err)
		}
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer logFile.Close()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()
	argv := append([]string{bin}, args...)
	proc, err := os.StartProcess(bin, argv, &os.ProcAttr{
		Dir:   "/",
		Files: []*os.File{devNull, logFile, logFile},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return 0, err
	}
	_ = proc.Release()
	return proc.Pid, nil
}

func writePidFile(path string, pid int) error {
	if path == "" {
		return fmt.Errorf("no pidfile path configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create pid dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
		return fmt.Errorf("write pidfile %s: %w", path, err)
	}
	return nil
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
		// syscall.Kill returns *os.SyscallError wrapping ESRCH/EPERM;
		// compare via errors.Is against syscall.Errno values.
		return !isNotExist(err)
	}
}

func isNotExist(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "no such process")
}

// Stop halts the router, systemd-first. In direct mode it SIGTERMs the
// pidfile pid, waits up to grace, then SIGKILLs. A missing or stale pidfile
// is cleaned and is not an error.
func Stop(ctx context.Context, opts Options, grace time.Duration) error {
	if systemdManaged(ctx, opts) {
		if out, err := opts.execFn()(ctx, opts.systemctl(), "--user", "stop", opts.unit()); err != nil {
			return fmt.Errorf("stop %s: %v: %s", opts.unit(), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return stopDirect(opts, grace)
}

func stopDirect(opts Options, grace time.Duration) error {
	pid, err := readPidFile(opts.PidPath)
	if err != nil {
		// Missing or unparsable pidfile: nothing to signal. Remove it so
		// a stale file never blocks a later start.
		_ = os.Remove(opts.PidPath)
		return nil
	}
	kill := opts.killFn()
	if !pidAlive(kill, pid) {
		_ = os.Remove(opts.PidPath)
		return nil
	}
	if err := kill(pid, syscall.SIGTERM); err != nil && !isNotExist(err) {
		return fmt.Errorf("signal %d: %w", pid, err)
	}
	deadline := time.Now().Add(grace)
	for pidAlive(kill, pid) {
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pidAlive(kill, pid) {
		if err := kill(pid, syscall.SIGKILL); err != nil && !isNotExist(err) {
			return fmt.Errorf("kill %d: %w", pid, err)
		}
		// Give the kernel a moment to reap before reporting success.
		for i := 0; i < 50 && pidAlive(kill, pid); i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
	_ = os.Remove(opts.PidPath)
	return nil
}

// Restart stops then starts the router, systemd-first. The direct-mode
// stop grace is DefaultStopGrace; the systemd path delegates to unit
// stop/start so readiness polling applies to both modes.
func Restart(ctx context.Context, opts Options, waitReady time.Duration) error {
	if systemdManaged(ctx, opts) {
		if out, err := opts.execFn()(ctx, opts.systemctl(), "--user", "restart", opts.unit()); err != nil {
			return fmt.Errorf("restart %s: %v: %s", opts.unit(), err, strings.TrimSpace(string(out)))
		}
		return waitLivez(ctx, opts, waitReady)
	}
	if err := stopDirect(opts, DefaultStopGrace); err != nil {
		return err
	}
	return startDirect(ctx, opts, waitReady)
}
