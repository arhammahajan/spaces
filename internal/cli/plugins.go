package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	hookcore "github.com/arhammahajan/spaces/internal/hooks/core"
)

// PluginLiveState counts live hook-store pane records per canonical agent ID.
type PluginLiveState struct {
	Fresh int
	Total int
}

// pluginHomeDir resolves the home directory for companion status checks:
// --home flag wins, then Getenv("HOME"), then os.UserHomeDir.
func pluginHomeDir(env Env, flagHome string) string {
	if strings.TrimSpace(flagHome) != "" {
		return strings.TrimSpace(flagHome)
	}
	if h := strings.TrimSpace(env.Getenv("HOME")); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// defaultPluginLive inspects the live hook store for plugin-sourced records,
// keyed by canonical agent ID. Freshness mirrors model.HookSummary.IsFreshPlugin
// TTLs so callers can distinguish active vs stale telemetry.
func defaultPluginLive() map[string]PluginLiveState {
	out := map[string]PluginLiveState{}
	var snap hookcore.Snapshot
	func() {
		defer func() { _ = recover() }()
		snap = hookcore.Summary()
	}()
	if snap.Panes == nil {
		return out
	}
	nowMs := snap.NowMs
	reg := agent.DefaultRegistry()
	for _, panes := range snap.Panes {
		for _, sum := range panes {
			if !strings.EqualFold(strings.TrimSpace(sum.Source), "plugin") {
				continue
			}
			id := reg.CanonicalID(sum.Agent)
			if id == "" {
				id = reg.CanonicalID(sum.App)
			}
			if id == "" {
				id = strings.ToLower(strings.TrimSpace(sum.Agent))
				if id == "" {
					id = strings.ToLower(strings.TrimSpace(sum.App))
				}
			}
			if id == "" {
				continue
			}
			st := out[id]
			st.Total++
			if isFreshPluginSummary(sum.State, sum.Kind, sum.TsMs, sum.PluginTsMs, sum.StateSinceMs, nowMs) {
				st.Fresh++
			}
			out[id] = st
		}
	}
	return out
}

// isFreshPluginSummary mirrors model.HookSummary.IsFreshPlugin TTLs over a
// hookcore typed PaneSummary without importing internal/model (which would
// widen the cli dependency surface).
func isFreshPluginSummary(state, kind string, tsMs, pluginTsMs, sinceMs, nowMs int64) bool {
	ts := pluginTsMs
	if ts <= 0 {
		ts = tsMs
	}
	if ts <= 0 {
		return false
	}
	age := nowMs - ts
	if age < 0 {
		age = -age
	}
	const workingStaleMs = int64(120_000)
	const doneTTLMs = int64(30 * 60_000)
	const failedTTLMs = int64(30 * 60_000)
	const blockTTLMs = int64(30 * 60_000)
	const authTTLMs = int64(60 * 60_000)
	switch state {
	case "working", "idle", "":
		return age <= workingStaleMs
	case "blocked", "waiting":
		since := sinceMs
		if since <= 0 {
			since = ts
		}
		d := nowMs - since
		if d < 0 {
			d = -d
		}
		if strings.EqualFold(strings.TrimSpace(kind), "auth") {
			return d <= authTTLMs
		}
		return d <= blockTTLMs
	case "done":
		since := sinceMs
		if since <= 0 {
			since = ts
		}
		d := nowMs - since
		if d < 0 {
			d = -d
		}
		return d <= doneTTLMs
	case "failed":
		since := sinceMs
		if since <= 0 {
			since = ts
		}
		d := nowMs - since
		if d < 0 {
			d = -d
		}
		return d <= failedTTLMs
	default:
		return age <= workingStaleMs
	}
}

// pluginLiveOf returns the injected live seam when set, else the hook-store default.
func pluginLiveOf(env Env) map[string]PluginLiveState {
	if env.PluginLive != nil {
		func() {
			defer func() { _ = recover() }()
		}()
		return env.PluginLive()
	}
	return defaultPluginLive()
}

// pluginLiveKey normalizes a registry agent ID for live-map lookups.
func pluginLiveKey(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// pluginStateFor maps install status + live telemetry onto one of the four
// companion states: fallback (not installed), active (fresh plugin records),
// stale (plugin records seen but none fresh), installed (installed, no live records).
func pluginStateFor(installed bool, live PluginLiveState) string {
	if !installed {
		return "fallback"
	}
	if live.Fresh > 0 {
		return "active"
	}
	if live.Total > 0 {
		return "stale"
	}
	return "installed"
}

type pluginListEntry struct {
	Agent        string   `json:"agent"`
	Plugin       string   `json:"plugin"`
	Version      string   `json:"version"`
	Runtime      string   `json:"runtime"`
	InstallPath  string   `json:"install_path,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Installed    bool     `json:"installed"`
}

type pluginStatusEntry struct {
	Agent       string `json:"agent"`
	Plugin      string `json:"plugin"`
	Version     string `json:"version"`
	State       string `json:"state"`
	Installed   bool   `json:"installed"`
	Path        string `json:"path,omitempty"`
	FreshPanes  int    `json:"fresh_panes"`
	TotalPanes  int    `json:"total_panes"`
	Detail      string `json:"detail"`
}

// PluginsHealthSummary is the default Env.PluginsHealth implementation used by
// `tmux-spaces doctor` and wired by cmd/tmux-spaces. It never errors when no
// plugins are installed: fallback mode always works, so the report stays
// informational.
func PluginsHealthSummary() (string, error) {
	return pluginsHealthSummary("", nil)
}

func pluginsHealthSummary(homeDir string, live map[string]PluginLiveState) (string, error) {
	plugins := agent.DefaultRegistry().CompanionPlugins()
	if len(plugins) == 0 {
		return "no companion plugins registered (fallback mode)", nil
	}
	if homeDir == "" {
		if h, err := os.UserHomeDir(); err == nil {
			homeDir = h
		} else {
			homeDir = os.Getenv("HOME")
		}
	}
	if live == nil {
		live = defaultPluginLive()
	}
	installed := 0
	active := 0
	for _, p := range plugins {
		cp := p.CompanionPlugin()
		if cp == nil {
			continue
		}
		st := cp.DetectCompanionStatus(homeDir)
		if st.Installed {
			installed++
		}
		if lv := live[pluginLiveKey(p.Spec().ID)]; lv.Fresh > 0 {
			active++
		}
	}
	switch {
	case installed == 0:
		return fmt.Sprintf("0/%d companion plugins installed (fallback mode)", len(plugins)), nil
	case active > 0:
		return fmt.Sprintf("%d/%d companion plugins installed, %d active", installed, len(plugins), active), nil
	default:
		return fmt.Sprintf("%d/%d companion plugins installed (no live plugin telemetry; screen fallback)", installed, len(plugins)), nil
	}
}

// cmdPlugins implements `tmux-spaces plugins ...`.
func cmdPlugins(args []string, env Env) int {
	sub := "list"
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		sub = rest[0]
		rest = rest[1:]
	}
	switch sub {
	case "list":
		return cmdPluginsList(rest, env)
	case "status":
		return cmdPluginsStatus(rest, env)
	case "install":
		return cmdPluginsInstall(rest, env)
	case "uninstall":
		return cmdPluginsUninstall(rest, env)
	case "-h", "--help", "help":
		env.outf("usage: tmux-spaces plugins [list|status|install|uninstall] ...\n")
		env.outf("  tmux-spaces plugins [--json] | list [--json]\n")
		env.outf("  tmux-spaces plugins status [--json]\n")
		env.outf("  tmux-spaces plugins install <agent|all> [--force] [--home DIR]\n")
		env.outf("  tmux-spaces plugins uninstall <agent|all> [--home DIR]\n")
		return ExitOK
	default:
		env.errf("tmux-spaces plugins: unknown subcommand %q (see 'tmux-spaces help plugins')\n", sub)
		return ExitUsage
	}
}

func cmdPluginsList(args []string, env Env) int {
	f, err := parseFlags(args, []string{"home"}, []string{"json"}, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces plugins list [--json] [--home DIR]\n")
			return ExitOK
		}
		env.errf("tmux-spaces plugins list: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces plugins list [--json] [--home DIR]\n")
		return ExitUsage
	}
	homeDir := pluginHomeDir(env, f.str["home"])
	plugins := agent.DefaultRegistry().CompanionPlugins()
	entries := make([]pluginListEntry, 0, len(plugins))
	for _, p := range plugins {
		cp := p.CompanionPlugin()
		if cp == nil {
			continue
		}
		spec := cp.CompanionSpec()
		st := cp.DetectCompanionStatus(homeDir)
		agentID := strings.TrimSpace(spec.AgentID)
		if agentID == "" {
			agentID = p.Spec().ID
		}
		path := strings.TrimSpace(spec.InstallDir)
		if st.Path != "" {
			path = st.Path
		}
		version := spec.Version
		if st.Version != "" {
			version = st.Version
		}
		entries = append(entries, pluginListEntry{
			Agent:        agentID,
			Plugin:       spec.PluginName,
			Version:      version,
			Runtime:      spec.Runtime,
			InstallPath:  path,
			Capabilities: append([]string(nil), spec.Capabilities...),
			Installed:    st.Installed,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Agent < entries[j].Agent })
	if f.set["json"] {
		raw, _ := json.Marshal(entries)
		env.outf("%s\n", raw)
		return ExitOK
	}
	if len(entries) == 0 {
		env.outf("no companion plugins registered (fallback mode)\n")
		return ExitOK
	}
	for _, e := range entries {
		state := "not installed (fallback)"
		if e.Installed {
			state = "installed"
		}
		caps := strings.Join(e.Capabilities, ",")
		if caps == "" {
			caps = "-"
		}
		env.outf("%-14s %-22s v%-10s %-8s %-10s caps:%s path:%s\n",
			e.Agent, e.Plugin, e.Version, e.Runtime, state, caps, e.InstallPath)
	}
	return ExitOK
}

func cmdPluginsStatus(args []string, env Env) int {
	f, err := parseFlags(args, []string{"home"}, []string{"json"}, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces plugins status [--json] [--home DIR]\n")
			return ExitOK
		}
		env.errf("tmux-spaces plugins status: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces plugins status [--json] [--home DIR]\n")
		return ExitUsage
	}
	homeDir := pluginHomeDir(env, f.str["home"])
	live := pluginLiveOf(env)
	plugins := agent.DefaultRegistry().CompanionPlugins()
	entries := make([]pluginStatusEntry, 0, len(plugins))
	for _, p := range plugins {
		cp := p.CompanionPlugin()
		if cp == nil {
			continue
		}
		spec := cp.CompanionSpec()
		agentID := strings.TrimSpace(spec.AgentID)
		if agentID == "" {
			agentID = p.Spec().ID
		}
		st := cp.DetectCompanionStatus(homeDir)
		lv := live[pluginLiveKey(agentID)]
		if lv.Total == 0 && lv.Fresh == 0 {
			// Fall back to spec-ID keying when the envelope agent differs.
			lv = live[pluginLiveKey(p.Spec().ID)]
		}
		state := pluginStateFor(st.Installed, lv)
		detail := st.Detail
		if detail == "" {
			switch state {
			case "active":
				detail = fmt.Sprintf("fresh plugin telemetry (%d panes)", lv.Fresh)
			case "stale":
				detail = "plugin records seen but stale; screen fallback"
			case "installed":
				detail = "installed; no live plugin telemetry"
			default:
				detail = "not installed; using fallback"
			}
		}
		version := spec.Version
		if st.Version != "" {
			version = st.Version
		}
		entries = append(entries, pluginStatusEntry{
			Agent:      agentID,
			Plugin:     spec.PluginName,
			Version:    version,
			State:      state,
			Installed:  st.Installed,
			Path:       st.Path,
			FreshPanes: lv.Fresh,
			TotalPanes: lv.Total,
			Detail:     detail,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Agent < entries[j].Agent })
	if f.set["json"] {
		raw, _ := json.Marshal(entries)
		env.outf("%s\n", raw)
		return ExitOK
	}
	if len(entries) == 0 {
		env.outf("no companion plugins registered (fallback mode)\n")
		return ExitOK
	}
	for _, e := range entries {
		env.outf("%-14s %-9s %s\n", e.Agent, e.State, e.Detail)
	}
	return ExitOK
}

func resolvePluginTargets(target string) ([]agent.Plugin, error) {
	all := agent.DefaultRegistry().CompanionPlugins()
	if strings.EqualFold(strings.TrimSpace(target), "all") {
		return all, nil
	}
	reg := agent.DefaultRegistry()
	if p, ok := reg.ResolveRuntime(target); ok && p.CompanionPlugin() != nil {
		return []agent.Plugin{p}, nil
	}
	// Match by companion AgentID / plugin name as a fallback.
	want := strings.ToLower(strings.TrimSpace(target))
	for _, p := range all {
		cp := p.CompanionPlugin()
		if cp == nil {
			continue
		}
		spec := cp.CompanionSpec()
		if strings.ToLower(strings.TrimSpace(spec.AgentID)) == want ||
			strings.ToLower(strings.TrimSpace(spec.PluginName)) == want ||
			strings.ToLower(strings.TrimSpace(p.Spec().ID)) == want {
			return []agent.Plugin{p}, nil
		}
	}
	return nil, fmt.Errorf("unknown companion plugin %q", target)
}

func cmdPluginsInstall(args []string, env Env) int {
	f, err := parseFlags(args, []string{"home"}, []string{"force", "json"}, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces plugins install <agent|all> [--force] [--home DIR]\n")
			return ExitOK
		}
		env.errf("tmux-spaces plugins install: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) != 1 {
		env.errf("usage: tmux-spaces plugins install <agent|all> [--force] [--home DIR]\n")
		return ExitUsage
	}
	targets, rerr := resolvePluginTargets(f.pos[0])
	if rerr != nil {
		env.errf("tmux-spaces plugins install: %s\n", rerr)
		return ExitUsage
	}
	if len(targets) == 0 {
		env.outf("no companion plugins registered (fallback mode)\n")
		return ExitOK
	}
	homeDir := pluginHomeDir(env, f.str["home"])
	failed := false
	for _, p := range targets {
		cp := p.CompanionPlugin()
		if cp == nil {
			continue
		}
		spec := cp.CompanionSpec()
		res, ierr := cp.InstallCompanion(agent.CompanionInstallOptions{
			HomeDir: homeDir,
			Force:   f.set["force"],
		})
		agentID := strings.TrimSpace(spec.AgentID)
		if agentID == "" {
			agentID = p.Spec().ID
		}
		if ierr != nil {
			env.errf("%s: install failed: %s\n", agentID, ierr)
			failed = true
			continue
		}
		verb := "installed"
		if res.Updated {
			verb = "updated"
		}
		env.outf("%s: %s %s at %s\n", agentID, verb, res.Version, res.Path)
	}
	if failed {
		return ExitError
	}
	return ExitOK
}

func cmdPluginsUninstall(args []string, env Env) int {
	f, err := parseFlags(args, []string{"home"}, []string{"json"}, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces plugins uninstall <agent|all> [--home DIR]\n")
			return ExitOK
		}
		env.errf("tmux-spaces plugins uninstall: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) != 1 {
		env.errf("usage: tmux-spaces plugins uninstall <agent|all> [--home DIR]\n")
		return ExitUsage
	}
	targets, rerr := resolvePluginTargets(f.pos[0])
	if rerr != nil {
		env.errf("tmux-spaces plugins uninstall: %s\n", rerr)
		return ExitUsage
	}
	if len(targets) == 0 {
		env.outf("no companion plugins registered (fallback mode)\n")
		return ExitOK
	}
	homeDir := pluginHomeDir(env, f.str["home"])
	failed := false
	for _, p := range targets {
		cp := p.CompanionPlugin()
		if cp == nil {
			continue
		}
		spec := cp.CompanionSpec()
		agentID := strings.TrimSpace(spec.AgentID)
		if agentID == "" {
			agentID = p.Spec().ID
		}
		if uerr := cp.UninstallCompanion(agent.CompanionInstallOptions{HomeDir: homeDir}); uerr != nil {
			env.errf("%s: uninstall failed: %s\n", agentID, uerr)
			failed = true
			continue
		}
		env.outf("%s: uninstalled\n", agentID)
	}
	if failed {
		return ExitError
	}
	return ExitOK
}
