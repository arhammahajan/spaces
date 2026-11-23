package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/osc"
)

// StripResumeArgs returns a copy of cmd with any prior resume flags or
// subcommands for app stripped (preserving cmd[0]).
func StripResumeArgs(app string, cmd []string) []string {
	if len(cmd) == 0 {
		return nil
	}
	var resumeFlag, resumeSubcmd string
	if p, ok := agent.DefaultRegistry().ResolveRuntime(app); ok {
		spec := p.Spec()
		resumeFlag = strings.TrimSpace(spec.ResumeFlag)
		resumeSubcmd = strings.TrimSpace(spec.ResumeSubcommand)
	}
	out := make([]string, 0, len(cmd))
	out = append(out, cmd[0])
	for i := 1; i < len(cmd); i++ {
		arg := cmd[i]
		if resumeFlag != "" {
			if arg == resumeFlag {
				if i+1 < len(cmd) && !strings.HasPrefix(cmd[i+1], "-") {
					i++
				}
				continue
			}
			if strings.HasPrefix(arg, resumeFlag+"=") {
				continue
			}
		}
		if resumeSubcmd != "" && arg == resumeSubcmd {
			if i+1 < len(cmd) && !strings.HasPrefix(cmd[i+1], "-") {
				i++
			}
			continue
		}
		out = append(out, arg)
	}
	return out
}

// cmdShim is the pane-side agent wrapper: it primes @spaces_state through
// working/idle on start, runs the child, then records done or failed on
// exit. --hold drops into an interactive shell afterwards; either way the
// child (or shell) exit code propagates.
//
// Flag parsing stops at the first positional so child arguments with
// leading dashes pass through untouched.
func cmdShim(args []string, env Env) int {
	hold := false
	profile := ""
	pos := []string{}
	onlyPos := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if onlyPos {
			pos = append(pos, a)
			continue
		}
		switch {
		case a == "--":
			onlyPos = true
		case a == "--hold":
			hold = true
		case a == "--profile":
			if i+1 >= len(args) {
				env.errf("tmux-spaces __shim: --profile requires a value\n")
				return ExitUsage
			}
			i++
			profile = strings.TrimSpace(args[i])
		case strings.HasPrefix(a, "--profile="):
			profile = strings.TrimSpace(strings.TrimPrefix(a, "--profile="))
		case a == "-h" || a == "--help":
			env.outf("usage: tmux-spaces __shim [--hold] [--profile P] <app> [<bin> [args...] | -- [args...]]\n")
			return ExitOK
		case len(a) > 1 && a[0] == '-':
			env.errf("tmux-spaces __shim: unknown flag %s\n", a)
			return ExitUsage
		default:
			pos = []string{a}
			onlyPos = true
		}
	}
	// Direct in-process harness profile launch:
	//   tmux-spaces __shim [--hold] --profile <P> <agent> [-- <args...>]
	// Backward-compatible explicit command launch:
	//   tmux-spaces __shim [--hold] [--profile <P>] <app> <bin> [args...]
	isDirectProfile := profile != "" && (len(pos) == 1 || (len(pos) >= 2 && pos[1] == "--"))
	if !isDirectProfile && len(pos) < 2 {
		env.errf("usage: tmux-spaces __shim [--hold] [--profile P] <app> [<bin> [args...] | -- [args...]]\n")
		return ExitUsage
	}
	rawAgent := pos[0]
	app := agent.DefaultRegistry().CLIDisplayApp(rawAgent)
	var bin string
	var childArgs []string
	if isDirectProfile {
		if len(pos) >= 2 && pos[1] == "--" {
			childArgs = pos[2:]
		}
	} else {
		bin = pos[1]
		childArgs = pos[2:]
	}

	oscEnabled := func() bool {
		inZellij := env.Getenv("ZELLIJ_SESSION_NAME") != ""
		inTmux := env.Getenv("TMUX") != "" && env.Getenv("TMUX_PANE") != ""
		if !inZellij && !inTmux {
			return false
		}
		switch env.Getenv("HARNESS_ATTN") {
		case "0", "false":
			return false
		}
		return osc.TtyWritable()
	}()

	setProfile := func(val string) {
		pane := env.Getenv("TMUX_PANE")
		if pane == "" || env.Client == nil {
			return
		}
		if val != "" {
			_ = env.Client.SetPaneOption(pane, "@spaces_profile", val)
		} else {
			_ = env.Client.UnsetPaneOption(pane, "@spaces_profile")
		}
	}

	setState := func(state, msg string) {
		pane := env.Getenv("TMUX_PANE")
		if pane == "" || env.Client == nil {
			return
		}
		_ = env.Client.SetPaneOption(pane, "@spaces_state", state)
		_ = env.Client.SetPaneOption(pane, "@spaces_app", app)
		_ = env.Client.UnsetPaneOption(pane, "@spaces_kind")
		if msg == "" {
			_ = env.Client.UnsetPaneOption(pane, "@spaces_msg")
		} else {
			_ = env.Client.SetPaneOption(pane, "@spaces_msg", msg)
		}
		_ = env.Client.SetPaneOption(pane, "@spaces_ts", itoa(int(env.Now().UnixMilli())))
	}

	// Prime the pane so it stays visible while idle, then run the child.
	if pane := env.Getenv("TMUX_PANE"); pane != "" && env.Client != nil {
		if plugin, ok := agent.DefaultRegistry().ResolveRuntime(rawAgent); ok && plugin.Spec().ID == "codex" {
			if id := plugin.SessionResolver().ExtractSessionIDFromArgs(childArgs); id != "" {
				_ = env.Client.SetPaneOption(pane, "@spaces_resume_session", id)
			}
		}
		var launchBase []string
		if isDirectProfile {
			canon := agent.DefaultRegistry().CanonicalID(rawAgent)
			if canon == "" {
				canon = rawAgent
			}
			launchBase = append([]string{env.Self, canon, "profile", profile}, childArgs...)
		} else {
			launchBase = append([]string{bin}, childArgs...)
		}
		baseCmd := StripResumeArgs(app, launchBase)
		if len(baseCmd) > 0 {
			if raw, err := json.Marshal(baseCmd); err == nil {
				_ = env.Client.SetPaneOption(pane, "@spaces_launch_cmd", string(raw))
			}
		}
	}
	if profile != "" {
		setProfile(profile)
	}
	setState("working", "")
	setState("idle", "")
	if oscEnabled {
		osc.BeginWithProfile(app, profile)
	}
	var code int
	if isDirectProfile {
		if env.RunHarnessChild != nil {
			code = env.RunHarnessChild(rawAgent, profile, childArgs)
		} else {
			code = env.RunChild(env.Self, append([]string{rawAgent, "profile", profile}, childArgs...))
		}
	} else {
		code = env.RunChild(bin, childArgs)
	}
	outcome := "done"
	msg := "finished"
	if code == 0 {
		setState("done", msg)
	} else {
		outcome = "failed"
		msg = fmt.Sprintf("exited with status %d", code)
		setState("failed", msg)
	}
	if oscEnabled {
		osc.FinishWithProfile(app, profile, code)
	}
	if env.RecordOutcome != nil {
		cwd := env.Getenv("PWD")
		if cwd == "" {
			if wd, err := os.Getwd(); err == nil {
				cwd = wd
			}
		}
		env.RecordOutcome(OutcomeRecord{
			Pane:    env.Getenv("TMUX_PANE"),
			App:     app,
			Profile: profile,
			Outcome: outcome,
			Text:    msg,
			CWD:     cwd,
			TsMs:    env.Now().UnixMilli(),
		})
	}
	if profile != "" {
		setProfile("")
	}
	if hold {
		return env.RunShell()
	}
	return code
}
