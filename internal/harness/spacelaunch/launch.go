package spacelaunch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/profiles"
)

// resolveProfile resolves a profile name to its launch spec and fully
// resolved process. Access levels apply deterministically (preselect only)
// unless interactive is set: background launches must never block on the
// picker, while the pane-side __shim-profile offers it like
// `harness profile` does. Pane-side resolution is authoritative.
func resolveProfile(name string, extraArgs []string, interactive bool) (profiles.Resolution, agents.Resolved, error) {
	paths := harnessenv.DefaultPaths()
	if agent, profile, qualified := strings.Cut(name, "/"); qualified {
		canonical, ok := agents.Normalize(agent)
		if !ok {
			return profiles.Resolution{}, agents.Resolved{}, fmt.Errorf("unknown agent: %s", agent)
		}
		name = canonical + "/" + profile
	}
	resolution, err := profiles.Resolve(name, paths)
	if err != nil {
		return profiles.Resolution{}, agents.Resolved{}, err
	}
	spec := profiles.LaunchSpecFromPaths(resolution, paths)
	if interactive {
		if err := ApplyAccessLevel(&spec); err != nil {
			if errors.Is(err, ErrAccessCancelled) {
				return profiles.Resolution{}, agents.Resolved{}, errAccessCancelledProfile
			}
			return profiles.Resolution{}, agents.Resolved{}, err
		}
	} else if err := PreselectAccessLevel(&spec); err != nil {
		return profiles.Resolution{}, agents.Resolved{}, err
	}
	router := harnessenv.RouterEnv(paths)
	var resolved agents.Resolved
	if resolution.NativeAntigravity || resolution.Source == profiles.SourceDefault {
		resolved, err = agents.Resolve(spec, paths, router, extraArgs)
	} else {
		resolved, err = agents.ResolveProfile(spec, paths, router, extraArgs)
	}
	if err != nil {
		return profiles.Resolution{}, agents.Resolved{}, err
	}
	return resolution, resolved, nil
}

var errAccessCancelledProfile = errors.New("launch cancelled")

// SpaceLaunchAgent implements
// `zj __space-launch-agent SESSION AGENT [--profile NAME] [ARGS...]`:
// open the agent in a new tab of an explicit session and return without
// attaching, so background plugin tasks can use it. With --profile the
// profile is resolved and handed to the pane-side __shim-profile; without
// it the agent binary runs raw under the plain __shim.
func SpaceLaunchAgent(args []string) int {
	if len(args) < 2 {
		return dief("usage: zj __space-launch-agent SESSION AGENT [--profile NAME] [ARGS...]")
	}
	if !zellijBinAvailable() {
		return dief("zellij not found on PATH (set ZELLIJ_BIN)")
	}
	session, agent := args[0], args[1]
	profile, extra := splitProfileFlag(args[2:])
	if !sessionExists(session) {
		return dief("session '%s' is not running", session)
	}
	shim := []string{"__shim", agent}
	label := agent
	if profile != "" {
		ref := agent + "/" + profile
		_, resolved, err := resolveProfile(ref, extra, false)
		if err != nil {
			return dief("%s", err)
		}
		// Profiles may prepend their own bin dirs; apply before checking.
		applyPathPrepend(resolved.PathPrepend)
		if !binOnPath(resolved.Binary) {
			return dief("agent binary '%s' not found on PATH", resolved.Binary)
		}
		label = profile
		shim = append([]string{agent, "profile", profile}, extra...)
	} else {
		bin := agent
		if agent == "cursor" {
			bin = "cursor-agent"
		}
		if !binOnPath(bin) {
			return dief("agent '%s' not found on PATH", bin)
		}
		shim = append(shim, bin)
		shim = append(shim, extra...)
	}
	layout, code := writeAgentLayout(label, plainPWD(), shim)
	if code != ExitOK {
		return code
	}
	if !sessionHasClient(session) {
		helperStarted, helper := ensureHeadlessClient(session)
		code = zellijRunExit("--session", session, "action",
			"new-tab", "--layout", layout)
		stopHeadlessClient(helperStarted, helper)
		return code
	}
	return zellijRunExit("--session", session, "action",
		"new-tab", "--layout", layout)
}

// splitProfileFlag extracts an optional `--profile NAME` pair from extra
// args, returning the profile name and the remaining args.
func splitProfileFlag(args []string) (string, []string) {
	profile := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--profile" && i+1 < len(args) {
			profile = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return profile, rest
}

func applyPathPrepend(dirs []string) {
	if len(dirs) == 0 {
		return
	}
	_ = os.Setenv("PATH", strings.Join(append(append([]string{}, dirs...), os.Getenv("PATH")), ":"))
}

// writeAgentLayout writes the one-tab KDL layout (tab-bar, agent pane,
// status-bar) mirroring zjcli's layout writer, with the pane running this
// binary's shim entry point instead of a raw agent.
func writeAgentLayout(label, cwd string, shimArgs []string) (string, int) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		runtime = "/tmp"
	}
	runtime = filepath.Join(runtime, "zj")
	if err := os.MkdirAll(runtime, 0o755); err != nil {
		return "", dief("%s", err)
	}
	f, err := os.CreateTemp(runtime, "agent-*.kdl")
	if err != nil {
		return "", dief("%s", err)
	}
	path := f.Name()
	var b strings.Builder
	b.WriteString("layout {\n")
	fmt.Fprintf(&b, "    tab name=\"%s\" {\n", kdlQuote(label))
	b.WriteString("        pane size=1 borderless=true {\n")
	b.WriteString("            plugin location=\"zellij:tab-bar\"\n")
	b.WriteString("        }\n")
	fmt.Fprintf(&b, "        pane cwd=\"%s\" command=\"%s\" {\n",
		kdlQuote(cwd), kdlQuote(selfPath()))
	// One args line carrying every argument: Zellij does not merge
	// repeated args lines (mirrors zjcli's layout writer).
	b.WriteString("            args")
	for _, arg := range shimArgs {
		fmt.Fprintf(&b, " \"%s\"", kdlQuote(arg))
	}
	b.WriteString("\n        }\n")
	b.WriteString("        pane size=1 borderless=true {\n")
	b.WriteString("            plugin location=\"zellij:status-bar\"\n")
	b.WriteString("        }\n")
	b.WriteString("    }\n")
	b.WriteString("}\n")
	if _, err := io.WriteString(f, b.String()); err != nil {
		_ = f.Close()
		return "", dief("%s", err)
	}
	if err := f.Close(); err != nil {
		return "", dief("%s", err)
	}
	return path, ExitOK
}

// kdlQuote mirrors the bash/zjcli quoting: backslash, double-quote,
// newline and tab escaping.
func kdlQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

// ensureHeadlessClient gives a clientless session a short-lived viewport
// so Zellij applies tab layouts; the tab survives its exit. Mirrors zjcli.
func ensureHeadlessClient(session string) (bool, interface{ Kill() error }) {
	helper := execCommand(zellijBin(), "terminal", session, "--headless",
		"--rows", "40", "--cols", "120", "--duration-secs", "30")
	helper.Stdout = io.Discard
	helper.Stderr = io.Discard
	if err := helper.Start(); err != nil {
		return false, nil
	}
	for i := 0; i < 30; i++ {
		if sessionHasClient(session) {
			break
		}
		sleep(100 * time.Millisecond)
	}
	return true, helper.Process
}

func stopHeadlessClient(started bool, proc interface{ Kill() error }) {
	if started && proc != nil {
		_ = proc.Kill()
	}
}
