// The eight whole-stack checks. Each returns (state, detail); Run supplies
// the row name and the panic guard. Every home- or environment-derived
// location comes from CheckOptions, so nothing here touches the real home
// when the options point at a temporary one.

package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/profiles"
	"github.com/arhammahajan/spaces/internal/harness/routerctl"
)

// checkRouter probes the router through routerctl.FetchStatus. Only a
// missing or malformed URL is a hard error (fail with the error as the
// detail); every probe failure degrades inside the Status, which maps to
// fail (not live) or warn (live but not ready).
func checkRouter(opts CheckOptions) (string, string) {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	st, err := routerctl.FetchStatus(ctx, opts.Router)
	if err != nil {
		return StateFail, err.Error()
	}
	if !st.Livez {
		return StateFail, fmt.Sprintf("router at %s not live", st.URL)
	}
	if !st.Ready {
		return StateWarn, fmt.Sprintf("live at %s but not ready", st.URL)
	}
	detail := fmt.Sprintf("live and ready; %d models", st.Models)
	if st.Version != "" {
		detail += "; version " + st.Version
	}
	return StateOK, detail
}

// checkRouterConfig validates the router config offline through
// routerctl.ValidateConfigWithOptions, on the injected or detected path.
func checkRouterConfig(opts CheckOptions) (string, string) {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	path := opts.routerConfigPath()
	if err := routerctl.ValidateConfigWithOptions(ctx, opts.Router, path); err != nil {
		return StateFail, err.Error()
	}
	return StateOK, "valid config at " + path
}

// checkProfiles loads the own profile store and reports the profile
// count. A missing store file is an empty store (ok); a parse failure is
// a fail.
func checkProfiles(opts CheckOptions) (string, string) {
	store, err := profiles.Open(opts.Paths)
	if err != nil {
		return StateFail, err.Error()
	}
	return StateOK, fmt.Sprintf("%d profiles", len(store.List()))
}

// checkAgents inventories the agent binaries through agents.Infos and
// lists the missing ones.
func checkAgents(opts CheckOptions) (string, string) {
	infos := agents.Infos(opts.Paths)
	checked := 0
	var missing []string
	for _, info := range infos {
		checked++
		if info.BinaryState != "ok" {
			missing = append(missing, info.Name)
		}
	}
	if len(missing) == 0 {
		return StateOK, fmt.Sprintf("%d/%d installed", checked, checked)
	}
	return StateWarn, "missing: " + strings.Join(missing, ", ")
}

// checkSpacesRuntime reads the hooks runtime directory: hooks.endpoint.json
// names the listener socket, which must exist, and the spool line count
// rides along in the detail. A missing endpoint is a warn (the listener
// starts lazily); a malformed endpoint, or one whose socket is missing,
// is a fail.
func checkSpacesRuntime(opts CheckOptions) (string, string) {
	dir := opts.spacesRuntimeDir()
	spool := spoolCount(filepath.Join(dir, "hooks-spool.jsonl"))
	endpointPath := filepath.Join(dir, "hooks.endpoint.json")
	data, err := os.ReadFile(endpointPath)
	if err != nil {
		if os.IsNotExist(err) {
			return StateWarn, fmt.Sprintf("no endpoint in %s; %d spooled events", dir, spool)
		}
		return StateFail, err.Error()
	}
	var endpoint struct {
		Socket string `json:"socket"`
	}
	if err := json.Unmarshal(data, &endpoint); err != nil || endpoint.Socket == "" {
		return StateFail, fmt.Sprintf("malformed endpoint %s", endpointPath)
	}
	if !fileExists(endpoint.Socket) {
		return StateFail, fmt.Sprintf("socket %s missing", endpoint.Socket)
	}
	return StateOK, fmt.Sprintf("socket %s; %d spooled events", endpoint.Socket, spool)
}

// spoolCount counts the non-blank lines of the hooks spool; a missing
// spool counts as zero.
func spoolCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// checkListener probes listener liveness: the pid recorded in
// hooks.server.lock, signaled with 0. The listener starts lazily, so a
// missing lock — or one without a parseable pid — is a warn; a parseable
// pid that no longer exists is a fail.
func checkListener(opts CheckOptions) (string, string) {
	lockPath := filepath.Join(opts.spacesRuntimeDir(), "hooks.server.lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return StateWarn, "no listener lock at " + lockPath
		}
		return StateFail, err.Error()
	}
	pid, ok := parsePid(string(data))
	if !ok {
		return StateWarn, "no parseable pid in " + lockPath
	}
	if pidAlive(opts.killFn(), pid) {
		return StateOK, fmt.Sprintf("listener pid %d alive", pid)
	}
	return StateFail, fmt.Sprintf("listener pid %d not running", pid)
}

// parsePid extracts a positive pid from lock file text: the whole
// trimmed text must be one integer, so "123abc" is not a pid.
func parsePid(data string) (int, bool) {
	pid, err := strconv.Atoi(strings.TrimSpace(data))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// pidAlive probes pid with signal 0. EPERM still counts as alive (the
// process exists but cannot be signaled); only ESRCH means gone — the
// same conservative semantics routerctl applies to its pidfile.
func pidAlive(kill func(int, syscall.Signal) error, pid int) bool {
	err := kill(pid, syscall.Signal(0))
	if err == nil {
		return true
	}
	return !errors.Is(err, syscall.ESRCH)
}

// checkPlugins inventories installed companion plugins against
// opts.Paths.Home. Companion plugins are optional: rich v2 telemetry is a
// fast path and screen scraping is always available as fallback, so this
// check is informational and always returns StateOK — never warn or fail —
// whether zero, some, or all companion plugins are installed.
func checkPlugins(opts CheckOptions) (string, string) {
	plugins := agent.DefaultRegistry().CompanionPlugins()
	if len(plugins) == 0 {
		return StateOK, "no companion plugins registered (fallback mode)"
	}
	home := opts.Paths.Home
	installed := 0
	var names []string
	for _, p := range plugins {
		cp := p.CompanionPlugin()
		if cp == nil {
			continue
		}
		st := cp.DetectCompanionStatus(home)
		if st.Installed {
			installed++
			id := strings.TrimSpace(cp.CompanionSpec().AgentID)
			if id == "" {
				id = p.Spec().ID
			}
			ver := strings.TrimSpace(st.Version)
			if ver == "" {
				ver = strings.TrimSpace(cp.CompanionSpec().Version)
			}
			if ver != "" {
				names = append(names, id+" v"+ver)
			} else {
				names = append(names, id)
			}
		}
	}
	sort.Strings(names)
	if installed == 0 {
		return StateOK, fmt.Sprintf("0/%d companion plugins installed (fallback mode)", len(plugins))
	}
	return StateOK, fmt.Sprintf("%d/%d companion plugins installed: %s", installed, len(plugins), strings.Join(names, ", "))
}

// entryNames are the launcher entry points this check verifies.
var entryNames = []string{"spaces"}

// checkEntries verifies the ~/.local/bin entry points: each must resolve
// through a symlink (or stand as a regular file) to an existing file. A
// missing or dangling entry is a fail: the shell and tmux integrations
// dispatch through these names.
func checkEntries(opts CheckOptions) (string, string) {
	dir := filepath.Join(opts.Paths.Home, ".local", "bin")
	var ok, bad []string
	for _, name := range entryNames {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			if target, lerr := os.Readlink(path); lerr == nil {
				bad = append(bad, fmt.Sprintf("%s dangles to %s", name, target))
			} else {
				bad = append(bad, name+" missing")
			}
			continue
		}
		if target, lerr := os.Readlink(path); lerr == nil {
			ok = append(ok, fmt.Sprintf("%s -> %s", name, target))
		} else {
			ok = append(ok, name+" (regular file)")
		}
	}
	if len(bad) > 0 {
		return StateFail, strings.Join(bad, "; ")
	}
	return StateOK, strings.Join(ok, "; ")
}

// checkTmux verifies the tmux integration files: ~/.local/bin/tmux-spaces and
// $ConfigHome/tmux/tmux.conf.
func checkTmux(opts CheckOptions) (string, string) {
	files := []struct{ label, path string }{
		{"plugin", filepath.Join(opts.Paths.Home, ".local", "bin", "tmux-spaces")},
		{"config", filepath.Join(opts.Paths.ConfigHome, "tmux", "tmux.conf")},
	}
	var missing []string
	for _, f := range files {
		if !fileExists(f.path) {
			missing = append(missing, f.label+" "+f.path)
		}
	}
	if len(missing) > 0 {
		return StateFail, "missing: " + strings.Join(missing, ", ")
	}
	return StateOK, "plugin and config present"
}

// fileExists reports whether path names an existing non-directory.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
