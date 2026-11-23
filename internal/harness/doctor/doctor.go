// Package doctor runs the whole-stack health checks behind
// `harness doctor`: one Check per area of the launcher/router/spaces
// stack, rendered as a single table.
//
// Run executes the checks in a stable order and never panics: every step
// runs behind a recover that converts a panic into a fail row, and router
// probes degrade to fail rows carrying the error as the detail.
//
// State semantics, shared by every check:
//
//	ok    the piece is healthy
//	warn  an optional or lazily created piece is currently absent: the
//	      hooks endpoint, the listener lock, uninstalled agent binaries
//	fail  a required piece is broken, corrupt, or missing: the router,
//	      its config, the profile store, the endpoint's socket, the
//	      listener process, the entry points, the tmux integration
//
// CheckOptions carries every home- or environment-derived location as an
// injectable field (plus the router probe and the signal seam), so tests
// run against t.TempDir homes and never touch the real one. The router,
// profile, and agent packages are imported read-only: the doctor only
// queries them.
package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/routerctl"
)

// Check states.
const (
	StateOK   = "ok"
	StateWarn = "warn"
	StateFail = "fail"
)

// Check is one doctor row: STATE, NAME, DETAIL. The JSON tags shape the
// --json output.
type Check struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// CheckOptions carries the injectable paths and seams. The zero value
// runs without panicking, but every home-relative check then resolves
// against empty paths; production callers fill Paths and Router the way
// cmd/harness builds its other subcommands, and tests point everything
// at temporary homes.
type CheckOptions struct {
	// Context bounds the router HTTP probe; nil means Background.
	Context context.Context
	// Paths resolves the home and XDG base directories.
	Paths harnessenv.Paths
	// Router drives the router status probe: URL, key, and the
	// routerctl hooks (Exec, Kill, PidPath) that keep managed-mode
	// detection off the real systemctl.
	Router routerctl.Options
	// RouterConfig overrides the router config path. When empty the
	// path is detected like routerctl.Detect does: $HARNESS_ROUTER_CONFIG,
	// then $ConfigHome/model-router/router-go.toml when it exists, then
	// the literal "router.toml".
	RouterConfig string
	// SpacesRuntime overrides the hooks runtime directory. When empty it
	// resolves to $SPACES_HOOKS_RUNTIME (with ${VAR} expansion, like the
	// hooks core), then ${XDG_RUNTIME_DIR:-/tmp}/spaces.
	SpacesRuntime string
	// Kill probes listener liveness; the default is syscall.Kill.
	Kill func(pid int, sig syscall.Signal) error
}

// routerConfigPath resolves the config path for the validation check.
func (o CheckOptions) routerConfigPath() string {
	if o.RouterConfig != "" {
		return o.RouterConfig
	}
	if cfg := harnessenv.ExpandHome(os.Getenv("HARNESS_ROUTER_CONFIG"), o.Paths.Home); cfg != "" {
		return cfg
	}
	cand := filepath.Join(o.Paths.ConfigHome, "model-router", "router-go.toml")
	if fileExists(cand) {
		return cand
	}
	return "router.toml"
}

// spacesRuntimeDir resolves the hooks runtime directory.
func (o CheckOptions) spacesRuntimeDir() string {
	if o.SpacesRuntime != "" {
		return o.SpacesRuntime
	}
	if configured := os.Getenv("SPACES_HOOKS_RUNTIME"); configured != "" {
		return os.ExpandEnv(configured)
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "spaces")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("spaces-%d", os.Getuid()))
}

// killFn returns the signal seam, defaulting to syscall.Kill.
func (o CheckOptions) killFn() func(int, syscall.Signal) error {
	if o.Kill != nil {
		return o.Kill
	}
	return syscall.Kill
}

// Run executes every check and returns the results in a stable order.
// Each step degrades to a fail row on error or panic; Run always returns
// one Check per step.
func Run(opts CheckOptions) []Check {
	steps := []struct {
		name string
		fn   func(CheckOptions) (string, string)
	}{
		{"router", checkRouter},
		{"router-config", checkRouterConfig},
		{"profiles", checkProfiles},
		{"agents", checkAgents},
		{"spaces-runtime", checkSpacesRuntime},
		{"spaces-listener", checkListener},
		{"entries", checkEntries},
		{"tmux", checkTmux},
		{"plugins", checkPlugins},
	}
	out := make([]Check, 0, len(steps))
	for _, step := range steps {
		state, detail := safeStep(step.name, step.fn, opts)
		out = append(out, Check{Name: step.name, State: state, Detail: detail})
	}
	return out
}

// safeStep runs one check, converting a panic into a fail row so Run
// always completes with one Check per step.
func safeStep(name string, fn func(CheckOptions) (string, string), opts CheckOptions) (state, detail string) {
	defer func() {
		if r := recover(); r != nil {
			state, detail = StateFail, fmt.Sprintf("check panicked: %v", r)
		}
	}()
	return fn(opts)
}
