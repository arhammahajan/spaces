package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/cli"
	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/doctor"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/modelpick"
	"github.com/arhammahajan/spaces/internal/harness/profiles"
	"github.com/arhammahajan/spaces/internal/harness/routerctl"
	usageagg "github.com/arhammahajan/spaces/internal/harness/usage"
	hookclient "github.com/arhammahajan/spaces/internal/hooks/client"
	"github.com/arhammahajan/spaces/internal/model"
)

const harnessProgName = "spaces"
const resumeAccessEnv = "SPACES_RESUME_ACCESS"

func runHarnessResume(argv0 string, args []string) int {
	if len(args) < 2 {
		return failHarnessUsage("resume requires an access level and a launch command")
	}
	if err := os.Setenv(resumeAccessEnv, args[0]); err != nil {
		return failHarnessUsage("%v", err)
	}
	defer os.Unsetenv(resumeAccessEnv)
	if code, handled := maybeRunHarnessCommand(argv0, args[1:]); handled {
		return code
	}
	return failHarnessUsage("invalid resume command: %s", args[1])
}

func preselectHarnessAccess(spec *harnessenv.LaunchSpec, args []string) error {
	level, resumed := os.LookupEnv(resumeAccessEnv)
	if !resumed && os.Getenv("HARNESS_AGENT") != "" {
		return nil
	}
	plugin, ok := agent.DefaultRegistry().ResolveRuntime(spec.Agent)
	if ok && level == "" {
		sessionID := plugin.SessionResolver().ExtractSessionIDFromArgs(args)
		if sessionID != "" {
			resumed = true
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, level, _ = agent.HarnessLaunchArgs(model.ConversationLaunchCommand(ctx, spec.Agent, sessionID))
		} else {
			native := plugin.Spec()
			for _, arg := range args {
				if arg == "--" {
					break
				}
				if arg == "--continue" || (native.ResumeSubcommand != "" && arg == native.ResumeSubcommand) ||
					(native.ResumeFlag != "" && (arg == native.ResumeFlag || strings.HasPrefix(arg, native.ResumeFlag+"="))) {
					resumed = true
					break
				}
			}
		}
	}
	if resumed {
		agents.ApplyResumeAccess(spec, level)
		return nil
	}
	return agents.PreselectAccessLevel(spec)
}

func chooseHarnessAccess(spec *harnessenv.LaunchSpec, args []string) error {
	if err := preselectHarnessAccess(spec, args); err != nil {
		return err
	}
	return agents.PromptAccessLevelForLaunch(spec)
}

func recordHarnessLaunch(spec harnessenv.LaunchSpec, profile string, args []string) {
	if os.Getenv("TMUX_PANE") == "" {
		return
	}
	level := spec.AccessLevel
	if level == "" {
		if os.Getenv("HARNESS_AGENT") != "" || len(agents.AccessLevels(spec.Agent)) == 0 {
			return
		}
		level = "default"
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	base := cli.StripResumeArgs(spec.Agent, append([]string{self}, args...))
	cmd := agent.HarnessResumeCommand(self, level, agent.HarnessProfileCommand(spec.Agent, profile, base[1:]...)...)
	if raw, err := json.Marshal(cmd); err == nil {
		hookclient.SetTmuxPaneOption("", "@spaces_launch_cmd", string(raw))
	}
}

// isSpacesSubcommand reports whether cmd is a built-in spaces/tmux-spaces
// subcommand (or hook subcommand) that must never be shadowed by HARNESS_AGENT
// or direct agent-name dispatch.
func isSpacesSubcommand(cmd string) bool {
	switch cmd {
	case "hook", "__hook", "session-hook", "hooks", "hook-server", "__space-hooks",
		"status-bar", "space", "spaces", "panes", "status", "prompt", "read",
		"wait", "next", "agent", "__shim", "__osc-watch", "doctor", "shell-init",
		"popup", "agents", "inbox", "conversations", "chats", "shared", "skills",
		"mcp", "tabs", "tab", "resurrect", "worktrees", "worktree", "wt", "help",
		"plugins", "plugin",
		"profile", "models", "usage", "launch", "harness-status", "harness-doctor",
		"-h", "--help":
		return true
	}
	return false
}

// maybeRunHarnessEnvAgent handles T3 Code-style invocations where HARNESS_AGENT
// selects the agent without a positional argument. Built-in spaces subcommands
// still take precedence if explicitly named.
func maybeRunHarnessEnvAgent(args []string) (int, bool) {
	if os.Getenv("HARNESS_AGENT") == "" {
		return 0, false
	}
	if len(args) > 0 && isSpacesSubcommand(args[0]) {
		return 0, false
	}
	return runHarnessAgentLaunch(args), true
}

// maybeRunHarnessCommand dispatches native coding-harness management and launch
// commands (`profile`, `agents`, `models`, `usage`, `doctor`, `launch`,
// `harness-status`, `harness-doctor`, and direct `<agent> [profile <name>]`).
// Existing `tmux-spaces` TUI/tmux subcommands (`tmux-spaces agents`,
// `tmux-spaces doctor`, `spaces status`, etc.) are preserved.
func maybeRunHarnessCommand(argv0 string, args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	prog := filepath.Base(argv0)
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "profile":
		return runHarnessProfile(rest), true
	case "models":
		return runHarnessModels(rest), true
	case "usage":
		return runHarnessUsage(rest), true
	case "harness-status":
		return runHarnessStatus(rest), true
	case "harness-doctor":
		return runHarnessDoctor(rest), true
	case "launch":
		if len(rest) == 0 {
			return failHarnessUsage("launch requires an agent name"), true
		}
		return runHarnessAgentLaunch(rest), true
	case "agents":
		// Keep `tmux-spaces agents` (used by tmux `prefix + a` popup) wired to the
		// Agents selector TUI; expose the CLI agent inventory for `spaces agents`
		// and whenever flags like `--json` are passed.
		if prog == "tmux-spaces" && len(rest) == 0 {
			return 0, false
		}
		return runHarnessAgents(rest), true
	case "doctor":
		// Keep `tmux-spaces doctor` wired to the tmux/server/hooks health check;
		// expose the whole-stack harness doctor via `spaces doctor` (and
		// `tmux-spaces harness-doctor`).
		if prog == "tmux-spaces" {
			return 0, false
		}
		return runHarnessDoctor(rest), true
	default:
		if !isSpacesSubcommand(cmd) {
			if _, ok := agents.Normalize(cmd); ok {
				return runHarnessAgentLaunch(args), true
			}
		}
		return 0, false
	}
}

func harnessUsage(w *os.File) {
	fmt.Fprintf(w, `Usage: spaces <agent> [args...]
       spaces <agent> profile <profile> [args...]
       spaces launch <agent> [profile <profile>] [args...]
       spaces profile <instance> [args...]
       spaces profile list [--json] [--agent <agent>]
       spaces profile show <name> [--agent <agent>] [--json] [--reveal]
       spaces profile get <name> <field> [--agent <agent>] [--reveal]
       spaces profile path [<name>] [--agent <agent>]
       spaces profile init
       spaces profile create <name> --agent <agent> [options]
       spaces profile edit <name> [--agent <agent>] [options]
       spaces profile enable <name> [--agent <agent>]
       spaces profile disable <name> [--agent <agent>]
       spaces profile clone <src> <dst> [--agent <agent>] [--no-seed]
       spaces profile rename <old> <new> [--agent <agent>]
       spaces profile remove <name> [--agent <agent>] [--purge]
       spaces profile describe <name> [--agent <agent>] [args...]
       spaces agents [--json]
       spaces models                     pick a model from the router (table when headless)
       spaces usage [--days N] [--by day|model] [--json] [PATH]
       spaces doctor [--json]            whole-stack health report
       spaces harness-status [--json]    router, agents, and profiles summary

Profile routing: each agent starts with a single built-in unmodified
"default" profile. In a single-profile setup (only "default"),
spaces <agent> [args...] launches it directly. Once you add one or more
custom profiles for an agent, spaces <agent> fails with a usage error
and asks you to choose one via
spaces <agent> profile <profile> [args...] (for example "default" or
your custom profile name). Profile names are scoped to their agent.
Use agent/name to select a profile wherever a name is accepted for lookup.

Agents:
  claude       codex        grok          cline
  opencode     pi           qwen          copilot
  kimi         kimi-cli     mcode         muse
  antigravity  fx           deepseek      kilo
  vibe         gemini

Aliases: agy = antigravity, dsh = deepseek, kimi-code = kimi
Set HARNESS_AGENT to choose the agent without a positional argument
(T3 Code invokes it this way; those launches skip the access picker).
Interactive launches offer the agent's access levels (full access, auto,
read-only, ...) before starting; preselect with HARNESS_ACCESS=<level>,
or answer "q" at the prompt to cancel.
All remaining arguments are passed unchanged to the agent.
`)
}

func failHarnessUsage(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "%s: %s\n", harnessProgName, fmt.Sprintf(format, args...))
	harnessUsage(os.Stderr)
	return 2
}

func harnessTopicHelp(topic string) int {
	switch topic {
	case "profile":
		fmt.Println("spaces <agent> [args...]")
		fmt.Println("spaces <agent> profile <profile> [args...]")
		fmt.Println("spaces profile <name> [args...]")
		fmt.Println("spaces profile list [--json] [--agent <agent>]")
		fmt.Println("spaces profile show <name> [--agent <agent>] [--json] [--reveal]")
		fmt.Println("spaces profile get <name> <field> [--agent <agent>] [--reveal]")
		fmt.Println("                    fields: binary, home-path, shadow-home, template-dir, env, env.<KEY>,")
		fmt.Println("                            args, launch-env, launch-args, launch-cmd, router, overlay,")
		fmt.Println("                            agent, enabled, display-name, source, seed-from, auth-store, config-file")
		fmt.Println("spaces profile path [<name>] [--agent <agent>]")
		fmt.Println("spaces profile init")
		fmt.Println("spaces profile create <name> --agent <agent> [--display-name N] [--shadow-home PATH]")
		fmt.Println("                    [--seed-from DIR] [--no-seed] [--env K=V]... [--arg A]... [--router]")
		fmt.Println("                    [--binary PATH] [--home-path PATH] [--overlay]")
		fmt.Println("spaces profile edit <name> [--agent <agent>] [--display-name N] [--binary PATH] [--home-path PATH]")
		fmt.Println("                    [--shadow-home PATH] [--no-shadow-home] [--seed-from DIR]")
		fmt.Println("                    [--env K=V]... [--unset-env K]... [--arg A]... [--clear-args]")
		fmt.Println("                    [--router] [--no-router] [--overlay] [--no-overlay]")
		fmt.Println("spaces profile enable <name> [--agent <agent>]")
		fmt.Println("spaces profile disable <name> [--agent <agent>]")
		fmt.Println("spaces profile clone <src> <dst> [--agent <agent>] [--no-seed]")
		fmt.Println("spaces profile rename <old> <new> [--agent <agent>]")
		fmt.Println("spaces profile remove <name> [--agent <agent>] [--purge]")
		fmt.Println("spaces profile describe <name> [--agent <agent>] [args...]")
	case "agents":
		fmt.Println("spaces agents [--json]")
	default:
		return failHarnessUsage("unknown help topic: %s", topic)
	}
	return 0
}

// --- agent launching -------------------------------------------------------

// Seams for unit tests: stub these to avoid exec'ing real binaries or
// touching the real home directory.
var (
	harnessDefaultPaths = harnessenv.DefaultPaths
	harnessForAgent     = profiles.ForAgent
	harnessLaunchAgent  = launchHarnessAgent
)

func profileResolutionNames(res []profiles.Resolution) []string {
	names := make([]string, 0, len(res))
	for _, r := range res {
		names = append(names, r.Name)
	}
	return names
}

func availableAgentProfiles(canonical string, paths harnessenv.Paths) []string {
	res, err := harnessForAgent(canonical, paths)
	if err != nil || len(res) == 0 {
		return nil
	}
	return profileResolutionNames(res)
}

func formatAvailableProfiles(canonical string, paths harnessenv.Paths) string {
	if names := availableAgentProfiles(canonical, paths); len(names) > 0 {
		return fmt.Sprintf("(available for %s: %s)", canonical, strings.Join(names, ", "))
	}
	return fmt.Sprintf("(no profiles found for %s)", canonical)
}

func runHarnessAgentLaunch(args []string) int {
	_, resumed := os.LookupEnv(resumeAccessEnv)
	if envAgent := os.Getenv("HARNESS_AGENT"); envAgent != "" && !resumed {
		canonical, ok := agents.Normalize(envAgent)
		if !ok {
			return failHarnessUsage("unknown agent: %s", envAgent)
		}
		paths := harnessDefaultPaths()
		return runHarnessBareAgent(canonical, paths, args, "")
	}
	if len(args) == 0 {
		return failHarnessUsage("agent name required")
	}
	agentName := args[0]
	agentArgs := args[1:]
	canonical, ok := agents.Normalize(agentName)
	if !ok {
		return failHarnessUsage("unknown agent: %s", agentName)
	}
	paths := harnessDefaultPaths()
	if len(agentArgs) > 0 && agentArgs[0] == "profile" {
		return runHarnessAgentProfileExplicit(canonical, paths, agentArgs)
	}
	res, err := harnessForAgent(canonical, paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	switch len(res) {
	case 0:
		return runHarnessBareAgent(canonical, paths, agentArgs, profiles.DefaultProfileName)
	case 1:
		if res[0].Source == profiles.SourceDefault {
			if !res[0].Customized {
				return runHarnessBareAgent(canonical, paths, agentArgs, res[0].Name)
			}
			return launchHarnessDefaultProfile(res[0], paths, agentArgs, false)
		}
		return launchHarnessResolvedProfile(res[0], paths, agentArgs, false)
	default:
		names := profileResolutionNames(res)
		fmt.Fprintf(os.Stderr, "%s: %s has %d profiles (%s); use \"spaces %s profile <profile> [args...]\" to choose one\n",
			harnessProgName, canonical, len(res), strings.Join(names, ", "), canonical)
		return 2
	}
}

func runHarnessBareAgent(canonical string, paths harnessenv.Paths, agentArgs []string, profile string) int {
	router := harnessenv.RouterEnv(paths)
	spec := harnessenv.LaunchSpec{Agent: canonical}
	if canonical == "antigravity" || canonical == "grok" {
		if err := profiles.MaterializeForLaunch(profiles.DefaultResolution(canonical), paths); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
	}
	if err := chooseHarnessAccess(&spec, agentArgs); err != nil {
		if errors.Is(err, agents.ErrAccessCancelled) {
			fmt.Fprintln(os.Stderr, "spaces: launch cancelled")
			return 0
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 2
	}
	resolved, err := agents.Resolve(spec, paths, router, agentArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	recordHarnessLaunch(spec, profile, agentArgs)
	return harnessLaunchAgent(resolved, canonical, profile)
}

func runHarnessAgentProfileExplicit(canonical string, paths harnessenv.Paths, argsAfterAgent []string) int {
	if len(argsAfterAgent) < 2 || argsAfterAgent[1] == "" {
		fmt.Fprintf(os.Stderr, "%s: spaces %s profile requires a profile name %s\n",
			harnessProgName, canonical, formatAvailableProfiles(canonical, paths))
		return 2
	}
	name := argsAfterAgent[1]
	rest := argsAfterAgent[2:]
	ref, err := qualifyProfileName(name, canonical)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	resolution, err := profiles.Resolve(ref, paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: profile %q for agent %s: %v %s\n",
			harnessProgName, name, canonical, err, formatAvailableProfiles(canonical, paths))
		return 2
	}
	if resolution.Source == profiles.SourceDefault {
		if !resolution.Customized {
			return runHarnessBareAgent(canonical, paths, rest, resolution.Name)
		}
		return launchHarnessDefaultProfile(resolution, paths, rest, false)
	}
	return launchHarnessResolvedProfile(resolution, paths, rest, false)
}

func launchHarnessDefaultProfile(resolution profiles.Resolution, paths harnessenv.Paths, rest []string, describe bool) int {
	spec := profiles.LaunchSpecFromPaths(resolution, paths)
	router := harnessenv.RouterEnv(paths)
	if describe {
		if err := preselectHarnessAccess(&spec, rest); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 2
		}
		resolved, err := agents.Resolve(spec, paths, router, rest)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
		fmt.Println(agents.Describe(resolved))
		return 0
	}
	if err := profiles.MaterializeForLaunch(resolution, paths); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if err := chooseHarnessAccess(&spec, rest); err != nil {
		if errors.Is(err, agents.ErrAccessCancelled) {
			fmt.Fprintln(os.Stderr, "spaces: launch cancelled")
			return 0
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 2
	}
	resolved, err := agents.Resolve(spec, paths, router, rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	recordHarnessLaunch(spec, resolution.Name, rest)
	return harnessLaunchAgent(resolved, spec.Agent, resolution.Name)
}

func launchHarnessResolvedProfile(resolution profiles.Resolution, paths harnessenv.Paths, rest []string, describe bool) int {
	spec := profiles.LaunchSpecFromPaths(resolution, paths)
	router := harnessenv.RouterEnv(paths)
	if err := preselectHarnessAccess(&spec, rest); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 2
	}
	resolved, err := agents.ResolveProfile(spec, paths, router, rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if describe {
		fmt.Println(agents.Describe(resolved))
		return 0
	}
	if err := profiles.MaterializeForLaunch(resolution, paths); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if err := agents.PromptAccessLevelForLaunch(&spec); err != nil {
		if errors.Is(err, agents.ErrAccessCancelled) {
			fmt.Fprintln(os.Stderr, "spaces: launch cancelled")
			return 0
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 2
	}
	resolved, err = agents.ResolveProfile(spec, paths, router, rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	recordHarnessLaunch(spec, resolution.Name, rest)
	return harnessLaunchAgent(resolved, spec.Agent, resolution.Name)
}

func launchHarnessAgent(resolved agents.Resolved, _, profile string) int {
	profile = strings.TrimSpace(profile)
	resolved.Env = withProfileEnv(resolved.Env, profile)
	resolved.UnsetEnv = append(resolved.UnsetEnv, resumeAccessEnv)
	if profile == "" {
		resolved.UnsetEnv = append(resolved.UnsetEnv, "HARNESS_PROFILE")
	}
	if err := agents.Launch(resolved); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	return 0
}

// runHarnessChild resolves a harness profile in-process, materializes overlays,
// builds the child environment via agents.BuildChildEnv (without mutating the
// current process environment), and runs the agent directly as a child process
// of `__shim`, eliminating the extra Go process hop.
func runHarnessChild(agentName, profile string, extraArgs []string) int {
	canonical, ok := agents.Normalize(agentName)
	if !ok {
		if cid := agent.DefaultRegistry().CanonicalID(agentName); cid != "" {
			canonical, ok = agents.Normalize(cid)
		}
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "%s: unknown agent: %s\n", harnessProgName, agentName)
		return 2
	}
	paths := harnessDefaultPaths()
	if profile == "" {
		profile = profiles.DefaultProfileName
	}
	ref, err := qualifyProfileName(profile, canonical)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 2
	}
	resolution, err := profiles.Resolve(ref, paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: profile %q for agent %s: %v\n", harnessProgName, profile, canonical, err)
		return 2
	}
	spec := profiles.LaunchSpecFromPaths(resolution, paths)
	router := harnessenv.RouterEnv(paths)
	if resolution.Source == profiles.SourceDefault && !resolution.Customized {
		if canonical == "antigravity" || canonical == "grok" {
			if err := profiles.MaterializeForLaunch(profiles.DefaultResolution(canonical), paths); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
				return 1
			}
		}
	} else {
		if err := profiles.MaterializeForLaunch(resolution, paths); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
	}
	if err := preselectHarnessAccess(&spec, extraArgs); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 2
	}
	var resolved agents.Resolved
	if resolution.Source == profiles.SourceDefault {
		resolved, err = agents.Resolve(spec, paths, router, extraArgs)
	} else {
		resolved, err = agents.ResolveProfile(spec, paths, router, extraArgs)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	recordHarnessLaunch(spec, resolution.Name, extraArgs)
	resolved.Env = withProfileEnv(resolved.Env, strings.TrimSpace(resolution.Name))
	childEnv := agents.BuildChildEnv(os.Environ(), resolved)
	bin, err := agents.ResolveBinary(resolved.Binary, resolved.PathPrepend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	cmd := exec.Command(bin, resolved.Args...)
	cmd.Env = childEnv
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	return 0
}

func withProfileEnv(env []string, profile string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == "HARNESS_PROFILE" {
			continue
		}
		out = append(out, kv)
	}
	if profile != "" {
		out = append(out, "HARNESS_PROFILE="+profile)
	}
	return out
}

// --- profiles ---------------------------------------------------------------

func runHarnessProfile(args []string) int {
	if len(args) == 0 {
		return failHarnessUsage("profile requires an instance or subcommand")
	}
	switch args[0] {
	case "-h", "--help":
		return harnessTopicHelp("profile")
	case "list", "--list":
		return runHarnessProfileList(args[1:])
	case "create":
		return runHarnessProfileCreate(args[1:])
	case "edit":
		return runHarnessProfileEdit(args[1:])
	case "enable":
		return runHarnessProfileSetEnabled(args[1:], true)
	case "disable":
		return runHarnessProfileSetEnabled(args[1:], false)
	case "clone":
		return runHarnessProfileClone(args[1:])
	case "rename":
		return runHarnessProfileRename(args[1:])
	case "remove":
		return runHarnessProfileRemove(args[1:])
	case "show":
		return runHarnessProfileShow(args[1:])
	case "get":
		return runHarnessProfileGet(args[1:])
	case "path":
		return runHarnessProfilePath(args[1:])
	case "init":
		return runHarnessProfileInit()
	}
	describe := false
	if args[0] == "--describe" || args[0] == "describe" {
		describe = true
		args = args[1:]
	}
	agentOpt := ""
	if describe {
		var err error
		agentOpt, args, err = extractAgentFlag(args)
		if err != nil {
			return failHarnessUsage("%v", err)
		}
	}
	if len(args) == 0 {
		return failHarnessUsage("profile requires an instance name")
	}
	paths := harnessDefaultPaths()
	resolution, code := resolveProfileQuery(args[0], agentOpt, paths)
	if code != 0 {
		return code
	}
	if resolution.Source == profiles.SourceDefault {
		return launchHarnessDefaultProfile(resolution, paths, args[1:], describe)
	}
	return launchHarnessResolvedProfile(resolution, paths, args[1:], describe)
}

func defaultBinaryLookup(paths harnessenv.Paths) func(string) string {
	return func(agent string) string {
		bin, err := agents.DefaultBinary(agent, paths)
		if err != nil {
			return ""
		}
		return bin
	}
}

func runHarnessProfilePath(args []string) int {
	agentOpt, pos, err := extractAgentFlag(args)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	paths := harnessDefaultPaths()
	if len(pos) == 0 {
		if agentOpt != "" {
			canon, ok := agents.Normalize(agentOpt)
			if !ok {
				return failHarnessUsage("unknown agent: %s", agentOpt)
			}
			fmt.Println(profiles.ProfileFilePath(paths, canon, profiles.DefaultProfileName))
			return 0
		}
		fmt.Println(profiles.ProfilesDir(paths))
		return 0
	}
	if len(pos) != 1 {
		return failHarnessUsage("path accepts at most one profile name")
	}
	if pos[0] == profiles.DefaultProfileName && agentOpt == "" {
		return failHarnessUsage("path default requires --agent <agent>")
	}
	res, code := resolveProfileQuery(pos[0], agentOpt, paths)
	if code != 0 {
		return code
	}
	fmt.Println(profiles.ProfileFilePath(paths, res.Profile.Agent, res.Name))
	return 0
}

func runHarnessProfileInit() int {
	paths := harnessDefaultPaths()
	store, err := profiles.Open(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if err := store.EnsureDefaults(defaultBinaryLookup(paths)); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	fmt.Println(profiles.ProfilesDir(paths))
	return 0
}

func extractAgentFlag(args []string) (string, []string, error) {
	var agentVal string
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--agent":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--agent requires a value")
			}
			agentVal = args[i+1]
			i++
		case strings.HasPrefix(a, "--agent="):
			agentVal = strings.TrimPrefix(a, "--agent=")
		default:
			out = append(out, a)
		}
	}
	return agentVal, out, nil
}

func runHarnessProfileList(args []string) int {
	jsonOut := false
	agentFilter := ""
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--json":
			jsonOut = true
			i++
		case a == "--agent":
			if i+1 >= len(args) {
				return failHarnessUsage("list --agent requires a value")
			}
			agentFilter = args[i+1]
			i += 2
		case len(a) > len("--agent=") && a[:len("--agent=")] == "--agent=":
			agentFilter = a[len("--agent="):]
			i++
		default:
			return failHarnessUsage("unknown list flag: %s", a)
		}
	}
	var entries []profiles.ListEntry
	var err error
	if agentFilter != "" {
		canon, ok := agents.Normalize(agentFilter)
		if !ok {
			return failHarnessUsage("unknown agent: %s", agentFilter)
		}
		entries, err = profiles.ListForAgent(canon, harnessDefaultPaths())
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
	} else {
		entries, err = profiles.ListAll(harnessDefaultPaths())
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
	}
	if entries == nil {
		entries = []profiles.ListEntry{}
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(entries); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
		return 0
	}
	fmt.Print(profiles.FormatPlain(entries))
	return 0
}

func runHarnessProfileCreate(args []string) int {
	fs := flag.NewFlagSet("spaces profile create", flag.ContinueOnError)
	agent := fs.String("agent", "", "canonical agent id (required)")
	displayName := fs.String("display-name", "", "human-readable name")
	shadowHome := fs.String("shadow-home", "", "shadow home path (default: preconfigured template)")
	seedFrom := fs.String("seed-from", "", "dotfiles subdirectory to seed the shadow home from")
	noSeed := fs.Bool("no-seed", false, "skip starting template / dotfiles seeding")
	useRouter := fs.Bool("router", false, "launch through the model router instead of the agent's own login/provider")
	noRouter := fs.Bool("no-router", false, "launch with the agent's own provider settings (default)")
	binaryPath := fs.String("binary", "", "explicit agent binary override")
	homePath := fs.String("home-path", "", "shared home for conversation/chat/MCP/skills overlay symlinks")
	useOverlay := fs.Bool("overlay", false, "enable shared MCPs/skills/chats overlay from default agent home (default)")
	noOverlay := fs.Bool("no-overlay", false, "disable shared MCPs/skills/chats overlay")
	var envs, argVals repeatedFlag
	fs.Var(&envs, "env", "extra environment KEY=VALUE (repeatable)")
	fs.Var(&argVals, "arg", "argument prepended to every launch (repeatable)")
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name = args[0]
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if name == "" {
		if fs.NArg() != 1 {
			return failHarnessUsage("create requires exactly one profile name")
		}
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return failHarnessUsage("create requires exactly one profile name")
	}
	if *agent == "" {
		return failHarnessUsage("create requires --agent")
	}
	canonical, ok := agents.Normalize(*agent)
	if !ok {
		return failHarnessUsage("unknown agent: %s", *agent)
	}
	if *useRouter && *noRouter {
		return failHarnessUsage("create accepts only one of --router and --no-router")
	}
	if *useOverlay && *noOverlay {
		return failHarnessUsage("create accepts only one of --overlay and --no-overlay")
	}
	env := map[string]string{}
	for _, kv := range envs {
		key, value, found := strings.Cut(kv, "=")
		if !found || key == "" {
			return failHarnessUsage("--env expects KEY=VALUE, got %q", kv)
		}
		env[key] = value
	}
	paths := harnessDefaultPaths()
	store, err := profiles.Open(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	binPath := *binaryPath
	if binPath == "" {
		if defBin, berr := agents.DefaultBinary(canonical, paths); berr == nil {
			binPath = defBin
		}
	}
	profile := profiles.Profile{
		Agent:       canonical,
		DisplayName: *displayName,
		Enabled:     true,
		ShadowHome:  *shadowHome,
		SeedFrom:    *seedFrom,
		Env:         env,
		PrependArgs: argVals,
		Router:      *useRouter,
		BinaryPath:  binPath,
		HomePath:    *homePath,
	}
	if *useOverlay {
		t := true
		profile.Overlay = &t
	} else if *noOverlay {
		f := false
		profile.Overlay = &f
	}
	if err := store.EnsureDefaults(defaultBinaryLookup(paths)); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if err := store.Create(name, profile, profiles.CreateOptions{NoSeed: *noSeed}); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if created, ok := store.Get(canonical + "/" + name); ok && created.Profile.ShadowHome != "" {
		fmt.Printf("created profile %s (agent %s, shadow home %s)\n", name, canonical, created.Profile.ShadowHome)
	} else {
		fmt.Printf("created profile %s (agent %s)\n", name, canonical)
	}
	return 0
}

func runHarnessProfileRemove(args []string) int {
	agentOpt, args, err := extractAgentFlag(args)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	purge := false
	var name string
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		default:
			if name != "" {
				return failHarnessUsage("remove takes exactly one profile name")
			}
			name = a
		}
	}
	if name == "" {
		return failHarnessUsage("remove requires a profile name")
	}
	ref, err := qualifyProfileName(name, agentOpt)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	paths := harnessDefaultPaths()
	store, err := profiles.Open(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if err := store.Remove(ref, purge); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	fmt.Printf("removed profile %s\n", name)
	return 0
}

type profileLaunchSnapshot struct {
	Binary   string            `json:"binary"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	UnsetEnv []string          `json:"unsetEnv"`
	Command  string            `json:"command"`
}

type profileShowSnapshot struct {
	Name              string                 `json:"name"`
	Source            string                 `json:"source"`
	Agent             string                 `json:"agent"`
	Enabled           bool                   `json:"enabled"`
	Customized        bool                   `json:"customized,omitempty"`
	DisplayName       string                 `json:"displayName"`
	BinaryPath        string                 `json:"binaryPath"`
	EffectiveBinary   string                 `json:"effectiveBinary"`
	HomePath          string                 `json:"homePath"`
	EffectiveHomePath string                 `json:"effectiveHomePath"`
	ShadowHomePath    string                 `json:"shadowHomePath"`
	TemplateDir       string                 `json:"templateDir"`
	SeedFrom          string                 `json:"seedFrom"`
	Overlay           bool                   `json:"overlay"`
	Router            bool                   `json:"router"`
	AuthStorePath     string                 `json:"authStorePath,omitempty"`
	AuthStorePresent  bool                   `json:"authStorePresent,omitempty"`
	Env               map[string]string      `json:"env"`
	PrependArgs       []string               `json:"prependArgs"`
	ConfigFile        string                 `json:"configFile"`
	EffectiveLaunch   *profileLaunchSnapshot `json:"effectiveLaunch,omitempty"`
}

func buildProfileSnapshot(resolution profiles.Resolution, paths harnessenv.Paths, reveal bool) profileShowSnapshot {
	p := resolution.Profile
	effBin := p.BinaryPath
	if effBin == "" {
		if defBin, err := agents.DefaultBinary(p.Agent, paths); err == nil {
			effBin = defBin
		}
	}
	effHome := p.HomePath
	if effHome == "" {
		effHome = profiles.DefaultSharedConfigHome(p.Agent, paths)
	}
	overlay := p.Overlay == nil || *p.Overlay
	envCopy := map[string]string{}
	for k, v := range p.Env {
		if !reveal && isSecretEnvKey(k) {
			v = "***"
		}
		envCopy[k] = v
	}
	argsCopy := append([]string{}, p.PrependArgs...)
	if argsCopy == nil {
		argsCopy = []string{}
	}
	snap := profileShowSnapshot{
		Name:              resolution.Name,
		Source:            resolution.Source,
		Agent:             p.Agent,
		Enabled:           p.Enabled,
		Customized:        resolution.Customized,
		DisplayName:       p.DisplayName,
		BinaryPath:        p.BinaryPath,
		EffectiveBinary:   effBin,
		HomePath:          p.HomePath,
		EffectiveHomePath: effHome,
		ShadowHomePath:    p.ShadowHome,
		TemplateDir:       profiles.DefaultTemplateDir(paths, p.Agent),
		SeedFrom:          p.SeedFrom,
		Overlay:           overlay,
		Router:            p.Router,
		Env:               envCopy,
		PrependArgs:       argsCopy,
		ConfigFile:        profiles.ProfileFilePath(paths, p.Agent, resolution.Name),
	}
	if authPath, present, ok := profiles.AuthStoreStatus(p, paths); ok {
		snap.AuthStorePath = authPath
		snap.AuthStorePresent = present
	}
	spec := profiles.LaunchSpecFromPaths(resolution, paths)
	router := harnessenv.RouterEnv(paths)
	var resolved agents.Resolved
	var rerr error
	if resolution.Source == profiles.SourceDefault {
		resolved, rerr = agents.Resolve(spec, paths, router, nil)
	} else {
		resolved, rerr = agents.ResolveProfile(spec, paths, router, nil)
	}
	if rerr == nil {
		launchEnv := map[string]string{}
		for _, kv := range resolved.Env {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			if !reveal && isSecretEnvKey(k) {
				v = "***"
			}
			launchEnv[k] = v
		}
		launchArgs := append([]string{}, resolved.Args...)
		if launchArgs == nil {
			launchArgs = []string{}
		}
		unsetEnv := append([]string{}, resolved.UnsetEnv...)
		if unsetEnv == nil {
			unsetEnv = []string{}
		}
		snap.EffectiveLaunch = &profileLaunchSnapshot{
			Binary:   resolved.Binary,
			Args:     launchArgs,
			Env:      launchEnv,
			UnsetEnv: unsetEnv,
			Command:  agents.Describe(resolved),
		}
	}
	return snap
}

func qualifyProfileName(name, agentOpt string) (string, error) {
	if agent, shortName, qualified := strings.Cut(name, "/"); qualified {
		canon, ok := agents.Normalize(agent)
		if !ok {
			return "", fmt.Errorf("unknown agent: %s", agent)
		}
		if agentOpt != "" {
			requested, ok := agents.Normalize(agentOpt)
			if !ok || requested != canon {
				return "", fmt.Errorf("--agent %q conflicts with profile %q", agentOpt, name)
			}
		}
		return canon + "/" + shortName, nil
	}
	if agentOpt != "" {
		canon, ok := agents.Normalize(agentOpt)
		if !ok {
			return "", fmt.Errorf("unknown agent: %s", agentOpt)
		}
		return canon + "/" + name, nil
	}
	return name, nil
}

func resolveProfileQuery(name, agentOpt string, paths harnessenv.Paths) (profiles.Resolution, int) {
	ref, err := qualifyProfileName(name, agentOpt)
	if err != nil {
		return profiles.Resolution{}, failHarnessUsage("%v", err)
	}
	res, err := profiles.Resolve(ref, paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return profiles.Resolution{}, 2
	}
	return res, 0
}

func runHarnessProfileShow(args []string) int {
	jsonOut := false
	reveal := false
	agentOpt := ""
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			jsonOut = true
		case a == "--reveal":
			reveal = true
		case a == "--agent":
			if i+1 >= len(args) {
				return failHarnessUsage("show --agent requires a value")
			}
			agentOpt = args[i+1]
			i++
		case strings.HasPrefix(a, "--agent="):
			agentOpt = strings.TrimPrefix(a, "--agent=")
		case strings.HasPrefix(a, "-"):
			return failHarnessUsage("unknown show flag: %s", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return failHarnessUsage("show requires exactly one profile name")
	}
	paths := harnessDefaultPaths()
	if pos[0] == profiles.DefaultProfileName && agentOpt == "" {
		fmt.Printf("name:         %s\n", profiles.DefaultProfileName)
		fmt.Printf("source:       %s\n", profiles.SourceDefault)
		fmt.Printf("profiles dir: %s\n", profiles.ProfilesDir(paths))
		fmt.Printf("note:         pass --agent <agent> to inspect $HOME/.harness/profiles/<agent>/default/profile.json\n")
		return 0
	}
	resolution, code := resolveProfileQuery(pos[0], agentOpt, paths)
	if code != 0 {
		return code
	}
	snap := buildProfileSnapshot(resolution, paths, reveal)
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(snap); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
		return 0
	}
	fmt.Printf("name:         %s\n", snap.Name)
	fmt.Printf("source:       %s\n", snap.Source)
	fmt.Printf("agent:        %s\n", snap.Agent)
	fmt.Printf("enabled:      %t\n", snap.Enabled)
	if snap.DisplayName != "" {
		fmt.Printf("display name: %s\n", snap.DisplayName)
	}
	if snap.BinaryPath != "" {
		fmt.Printf("binary:       %s\n", snap.BinaryPath)
	} else if snap.EffectiveBinary != "" {
		fmt.Printf("binary:       %s (default)\n", snap.EffectiveBinary)
	}
	if snap.HomePath != "" {
		fmt.Printf("home path:    %s\n", snap.HomePath)
	} else if snap.EffectiveHomePath != "" {
		fmt.Printf("home path:    %s (default)\n", snap.EffectiveHomePath)
	}
	if snap.ShadowHomePath != "" {
		fmt.Printf("shadow home:  %s\n", snap.ShadowHomePath)
	}
	fmt.Printf("template dir: %s\n", snap.TemplateDir)
	if profiles.SupportsOverlay(snap.Agent) {
		fmt.Printf("overlay:      %t\n", snap.Overlay)
		if snap.Overlay && snap.EffectiveHomePath != "" {
			fmt.Printf("shared home:  %s\n", snap.EffectiveHomePath)
		}
	}
	if snap.AuthStorePath != "" {
		state := "not logged in"
		if snap.AuthStorePresent {
			state = "present"
		}
		fmt.Printf("auth store:   %s (%s)\n", snap.AuthStorePath, state)
	}
	fmt.Printf("router:       %t\n", snap.Router)
	if len(snap.PrependArgs) > 0 {
		fmt.Printf("args:         %s\n", strings.Join(snap.PrependArgs, " "))
	}
	if len(snap.Env) > 0 {
		fmt.Printf("env:\n")
		keys := make([]string, 0, len(snap.Env))
		for k := range snap.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %s=%s\n", k, snap.Env[k])
		}
	}
	fmt.Printf("config file:  %s\n", snap.ConfigFile)
	return 0
}

func runHarnessProfileGet(args []string) int {
	reveal := false
	agentOpt := ""
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--reveal":
			reveal = true
		case a == "--agent":
			if i+1 >= len(args) {
				return failHarnessUsage("get --agent requires a value")
			}
			agentOpt = args[i+1]
			i++
		case strings.HasPrefix(a, "--agent="):
			agentOpt = strings.TrimPrefix(a, "--agent=")
		case strings.HasPrefix(a, "-"):
			return failHarnessUsage("unknown get flag: %s", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return failHarnessUsage("get requires <profile> <field> [--agent <agent>]")
	}
	name, field := pos[0], pos[1]
	paths := harnessDefaultPaths()
	if name == profiles.DefaultProfileName && agentOpt == "" {
		return failHarnessUsage("get default requires --agent <agent>")
	}
	resolution, code := resolveProfileQuery(name, agentOpt, paths)
	if code != 0 {
		return code
	}
	snap := buildProfileSnapshot(resolution, paths, reveal)
	if strings.HasPrefix(field, "env.") {
		key := strings.TrimPrefix(field, "env.")
		v, ok := snap.Env[key]
		if !ok {
			return 1
		}
		fmt.Println(v)
		return 0
	}
	switch strings.ToLower(field) {
	case "name":
		fmt.Println(snap.Name)
	case "agent":
		fmt.Println(snap.Agent)
	case "source":
		fmt.Println(snap.Source)
	case "enabled":
		fmt.Printf("%t\n", snap.Enabled)
	case "customized":
		fmt.Printf("%t\n", snap.Customized)
	case "display-name", "displayname":
		fmt.Println(snap.DisplayName)
	case "binary", "binary-path", "binarypath":
		fmt.Println(snap.EffectiveBinary)
	case "home", "home-path", "homepath", "central-home":
		fmt.Println(snap.EffectiveHomePath)
	case "shadow", "shadow-home", "shadowhome", "shadowhomepath":
		fmt.Println(snap.ShadowHomePath)
	case "template", "template-dir", "templatedir":
		fmt.Println(snap.TemplateDir)
	case "seed", "seed-from", "seedfrom":
		fmt.Println(snap.SeedFrom)
	case "overlay":
		fmt.Printf("%t\n", snap.Overlay)
	case "router":
		fmt.Printf("%t\n", snap.Router)
	case "auth-store", "authstore":
		fmt.Println(snap.AuthStorePath)
	case "config-file", "configfile", "config":
		fmt.Println(snap.ConfigFile)
	case "args", "prepend-args", "prependargs":
		for _, a := range snap.PrependArgs {
			fmt.Println(a)
		}
	case "env":
		keys := make([]string, 0, len(snap.Env))
		for k := range snap.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("%s=%s\n", k, snap.Env[k])
		}
	case "launch-args", "launchargs":
		if snap.EffectiveLaunch != nil {
			for _, a := range snap.EffectiveLaunch.Args {
				fmt.Println(a)
			}
		}
	case "launch-env", "launchenv":
		if snap.EffectiveLaunch != nil {
			keys := make([]string, 0, len(snap.EffectiveLaunch.Env))
			for k := range snap.EffectiveLaunch.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("%s=%s\n", k, snap.EffectiveLaunch.Env[k])
			}
		}
	case "launch-cmd", "launchcmd", "command":
		if snap.EffectiveLaunch != nil {
			fmt.Println(snap.EffectiveLaunch.Command)
		}
	default:
		return failHarnessUsage("unknown profile field: %s", field)
	}
	return 0
}

func isSecretEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, sub := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "AUTH", "CREDENTIAL"} {
		if strings.Contains(upper, sub) {
			return true
		}
	}
	return false
}

func runHarnessProfileEdit(args []string) int {
	fs := flag.NewFlagSet("spaces profile edit", flag.ContinueOnError)
	agent := fs.String("agent", "", "agent that owns the profile")
	displayName := fs.String("display-name", "", "human-readable name")
	binaryPath := fs.String("binary", "", "explicit agent binary override")
	homePath := fs.String("home-path", "", "shared home for conversation/chat overlay symlinks")
	shadowHome := fs.String("shadow-home", "", "shadow home path")
	noShadowHome := fs.Bool("no-shadow-home", false, "clear the shadow home")
	seedFrom := fs.String("seed-from", "", "dotfiles subdirectory to seed the shadow home from")
	_ = fs.Bool("router", false, "launch through the model router")
	_ = fs.Bool("no-router", false, "launch with the agent's own provider settings")
	_ = fs.Bool("overlay", false, "enable shared-conversation / isolated-auth overlay")
	_ = fs.Bool("no-overlay", false, "disable shared-conversation / isolated-auth overlay")
	clearArgs := fs.Bool("clear-args", false, "clear prepended args (combined with --arg to replace)")
	var envs, unsetEnvs, argVals repeatedFlag
	fs.Var(&envs, "env", "extra environment KEY=VALUE (repeatable)")
	fs.Var(&unsetEnvs, "unset-env", "remove environment KEY (repeatable)")
	fs.Var(&argVals, "arg", "argument prepended to every launch (repeatable)")
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name = args[0]
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if name == "" {
		if fs.NArg() != 1 {
			return failHarnessUsage("edit requires exactly one profile name")
		}
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return failHarnessUsage("edit requires exactly one profile name")
	}
	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
	if setFlags["router"] && setFlags["no-router"] {
		return failHarnessUsage("edit accepts only one of --router and --no-router")
	}
	if setFlags["overlay"] && setFlags["no-overlay"] {
		return failHarnessUsage("edit accepts only one of --overlay and --no-overlay")
	}
	if setFlags["shadow-home"] && *noShadowHome {
		return failHarnessUsage("edit accepts only one of --shadow-home and --no-shadow-home")
	}
	var patch profiles.EditPatch
	if setFlags["display-name"] {
		v := *displayName
		patch.DisplayName = &v
	}
	if setFlags["binary"] {
		v := *binaryPath
		patch.BinaryPath = &v
	}
	if setFlags["home-path"] {
		v := *homePath
		patch.HomePath = &v
	}
	if *noShadowHome {
		patch.ClearShadowHome = true
	} else if setFlags["shadow-home"] {
		v := *shadowHome
		patch.ShadowHome = &v
	}
	if setFlags["seed-from"] {
		v := *seedFrom
		patch.SeedFrom = &v
	}
	if len(envs) > 0 {
		m := map[string]string{}
		for _, kv := range envs {
			key, value, found := strings.Cut(kv, "=")
			if !found || key == "" {
				return failHarnessUsage("--env expects KEY=VALUE, got %q", kv)
			}
			m[key] = value
		}
		patch.SetEnv = m
	}
	if len(unsetEnvs) > 0 {
		patch.UnsetEnv = append([]string{}, unsetEnvs...)
	}
	if *clearArgs && len(argVals) > 0 {
		patch.SetPrependArgs = true
		patch.PrependArgs = append([]string{}, argVals...)
	} else if *clearArgs {
		patch.ClearPrependArgs = true
	} else if len(argVals) > 0 {
		patch.PrependArgs = append([]string{}, argVals...)
	}
	if setFlags["router"] {
		v := true
		patch.Router = &v
	} else if setFlags["no-router"] {
		v := false
		patch.Router = &v
	}
	if setFlags["overlay"] {
		v := true
		patch.Overlay = &v
	} else if setFlags["no-overlay"] {
		v := false
		patch.Overlay = &v
	}
	ref, err := qualifyProfileName(name, *agent)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	paths := harnessDefaultPaths()
	store, err := profiles.Open(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if err := store.Edit(ref, patch); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	fmt.Printf("edited profile %s\n", name)
	return 0
}

func runHarnessProfileSetEnabled(args []string, enabled bool) int {
	action := "disable"
	if enabled {
		action = "enable"
	}
	agentOpt, args, err := extractAgentFlag(args)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	if len(args) != 1 {
		return failHarnessUsage("%s requires exactly one profile name", action)
	}
	ref, err := qualifyProfileName(args[0], agentOpt)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	store, err := profiles.Open(harnessDefaultPaths())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if err := store.SetEnabled(ref, enabled); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	fmt.Printf("%sd profile %s\n", action, ref)
	return 0
}

func runHarnessProfileClone(args []string) int {
	agentOpt, args, err := extractAgentFlag(args)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	noSeed := false
	var positional []string
	for _, a := range args {
		switch a {
		case "--no-seed":
			noSeed = true
		case "-h", "--help":
			return harnessTopicHelp("profile")
		default:
			if strings.HasPrefix(a, "-") {
				return failHarnessUsage("unknown clone flag: %s", a)
			}
			positional = append(positional, a)
		}
	}
	if len(positional) != 2 {
		return failHarnessUsage("clone requires exactly two profile names")
	}
	ref, err := qualifyProfileName(positional[0], agentOpt)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	paths := harnessDefaultPaths()
	store, err := profiles.Open(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if err := store.Clone(ref, positional[1], profiles.CreateOptions{NoSeed: noSeed}); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	fmt.Printf("cloned profile %s to %s\n", positional[0], positional[1])
	return 0
}

func runHarnessProfileRename(args []string) int {
	agentOpt, args, err := extractAgentFlag(args)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	if len(args) != 2 {
		return failHarnessUsage("rename requires exactly two profile names")
	}
	ref, err := qualifyProfileName(args[0], agentOpt)
	if err != nil {
		return failHarnessUsage("%v", err)
	}
	paths := harnessDefaultPaths()
	store, err := profiles.Open(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if err := store.Rename(ref, args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	fmt.Printf("renamed profile %s to %s\n", args[0], args[1])
	return 0
}

// --- router / agents / usage / models / doctor ------------------------------

func harnessRouterOptions() (routerctl.Options, harnessenv.Paths, harnessenv.RouterSettings) {
	paths := harnessDefaultPaths()
	router := harnessenv.RouterEnv(paths)
	return routerctl.Detect(paths, router), paths, router
}

func printRouterStatus(status routerctl.Status) {
	fmt.Printf("url:      %s\n", status.URL)
	fmt.Printf("managed:  %s\n", status.Managed)
	fmt.Printf("live:     %t\n", status.Livez)
	fmt.Printf("ready:    %t\n", status.Ready)
	if status.Version != "" {
		fmt.Printf("version:  %s\n", status.Version)
	}
	fmt.Printf("models:   %d\n", status.Models)
	if status.DiagnosticsNote != "" {
		fmt.Printf("diagnostics: %s\n", status.DiagnosticsNote)
	}
}

func runHarnessAgents(args []string) int {
	jsonOut := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "-h", "--help":
			return harnessTopicHelp("agents")
		default:
			return failHarnessUsage("unknown agents flag: %s", a)
		}
	}
	infos := agents.Infos(harnessDefaultPaths())
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(infos); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
		return 0
	}
	for _, info := range infos {
		state := info.BinaryState
		if state == "" {
			state = "?"
		}
		fmt.Printf("%-18s %-8s %s\n", info.Name, state, info.Binary)
	}
	return 0
}

type combinedHarnessStatus struct {
	Router   routerctl.Status     `json:"router"`
	Profiles []profiles.ListEntry `json:"profiles"`
	Agents   []agents.AgentInfo   `json:"agents"`
}

func runHarnessStatus(args []string) int {
	jsonOut := false
	for _, a := range args {
		if a != "--json" {
			return failHarnessUsage("unknown harness-status flag: %s", a)
		}
		jsonOut = true
	}
	ctx := context.Background()
	opts, paths, _ := harnessRouterOptions()
	status, err := routerctl.FetchStatus(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	entries, err := profiles.ListAll(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	infos := agents.Infos(paths)
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(combinedHarnessStatus{Router: status, Profiles: entries, Agents: infos}); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
		return 0
	}
	printRouterStatus(status)
	installed := 0
	for _, info := range infos {
		if info.BinaryState == "ok" {
			installed++
		}
	}
	fmt.Printf("agents:   %d/%d installed\n", installed, len(infos))
	fmt.Printf("profiles: %d\n", len(entries))
	if !status.Livez {
		return 1
	}
	return 0
}

func defaultUsagePath(paths harnessenv.Paths) string {
	return filepath.Join(paths.StateHome, "model-router", "accounting.jsonl")
}

func runHarnessUsage(args []string) int {
	fs := flag.NewFlagSet("spaces usage", flag.ContinueOnError)
	days := fs.Int("days", 0, "limit to the last N days (0 = all)")
	by := fs.String("by", "day", "grouping: day or model")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *by != "day" && *by != "model" {
		return failHarnessUsage("unknown --by grouping: %s", *by)
	}
	path := fs.Arg(0)
	if path == "" {
		path = defaultUsagePath(harnessDefaultPaths())
	}
	entries, err := usageagg.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	summary := usageagg.Aggregate(entries, time.Now(), *days)
	if *jsonOut {
		fmt.Println(usageagg.RenderJSON(summary))
		return 0
	}
	fmt.Print(usageagg.RenderTable(summary, *by))
	return 0
}

func runHarnessModels(args []string) int {
	for _, a := range args {
		return failHarnessUsage("unknown models flag: %s", a)
	}
	paths := harnessDefaultPaths()
	router := harnessenv.RouterEnv(paths)
	models, err := modelpick.Fetch(router, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
		return 1
	}
	if len(models) == 0 {
		fmt.Fprintf(os.Stderr, "%s: the router serves no models\n", harnessProgName)
		return 1
	}
	if picked, ok := modelpick.Pick(models); ok {
		fmt.Println(picked.ID)
		return 0
	}
	fmt.Print(modelpick.Render(models))
	return 0
}

func runHarnessDoctor(args []string) int {
	jsonOut := false
	for _, a := range args {
		if a != "--json" {
			return failHarnessUsage("unknown doctor flag: %s", a)
		}
		jsonOut = true
	}
	opts, paths, _ := harnessRouterOptions()
	checks := doctor.Run(doctor.CheckOptions{
		Context: context.Background(),
		Paths:   paths,
		Router:  opts,
	})
	if jsonOut {
		out, err := doctor.RenderJSON(checks)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", harnessProgName, err)
			return 1
		}
		fmt.Print(out)
		return 0
	}
	fmt.Print(doctor.Render(checks))
	return 0
}

type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }

func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}
