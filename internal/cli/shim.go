package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/osc"
)

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
			env.outf("usage: tmux-spaces __shim [--hold] [--profile P] <app> <bin> [args...]\n")
			return ExitOK
		case len(a) > 1 && a[0] == '-':
			env.errf("tmux-spaces __shim: unknown flag %s\n", a)
			return ExitUsage
		default:
			pos = []string{a}
			onlyPos = true
		}
	}
	if len(pos) < 2 {
		env.errf("usage: tmux-spaces __shim [--hold] [--profile P] <app> <bin> [args...]\n")
		return ExitUsage
	}
	app := agent.DefaultRegistry().CLIDisplayApp(pos[0])
	bin := pos[1]
	childArgs := pos[2:]

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
	if profile != "" {
		setProfile(profile)
	}
	setState("working", "")
	setState("idle", "")
	if oscEnabled {
		osc.BeginWithProfile(app, profile)
	}
	code := env.RunChild(bin, childArgs)
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
