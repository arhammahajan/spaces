package cli

var validPaneStates = map[string]bool{
	"idle": true, "working": true, "done": true,
	"blocked": true, "error": true, "failed": true, "clear": true,
}

// cmdStatus sets @spaces_* options on the current (or target) tmux pane.
func cmdStatus(args []string, env Env) int {
	f, err := parseFlags(args,
		[]string{"app", "profile", "kind", "msg", "pane"},
		nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces status <idle|working|done|blocked|error|clear> [--app A] [--profile P] [--kind K] [--msg M] [--pane %%N]\n")
			return ExitOK
		}
		env.errf("tmux-spaces status: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) != 1 {
		env.errf("usage: tmux-spaces status <idle|working|done|blocked|error|clear> [--app A] [--profile P] [--kind K] [--msg M] [--pane %%N]\n")
		return ExitUsage
	}
	state := f.pos[0]
	if !validPaneStates[state] {
		env.errf("tmux-spaces status: invalid state %q (idle, working, done, blocked, error, clear)\n", state)
		return ExitUsage
	}
	if state == "failed" {
		state = "error"
	}
	client, cerr := env.requireClient()
	if cerr != nil {
		env.errf("tmux-spaces status: %s\n", cerr)
		return ExitError
	}
	pane := f.str["pane"]
	if pane == "" {
		pane = env.Getenv("TMUX_PANE")
	}
	if pane == "" {
		env.errf("tmux-spaces status: no pane (pass --pane or run inside tmux)\n")
		return ExitError
	}
	if state == "clear" {
		for _, opt := range []string{
			"@spaces_state", "@spaces_app", "@spaces_profile", "@spaces_kind",
			"@spaces_msg", "@spaces_ts", "@spaces_source", "@spaces_title",
			"@spaces_resume_session", "@spaces_resume_agent", "@spaces_prior_outcome",
			"@spaces_launch_cmd",
		} {
			if uerr := client.UnsetPaneOption(pane, opt); uerr != nil {
				env.errf("tmux-spaces status: %s\n", uerr)
				return ExitError
			}
		}
		return ExitOK
	}
	if serr := client.SetPaneOption(pane, "@spaces_state", state); serr != nil {
		env.errf("tmux-spaces status: %s\n", serr)
		return ExitError
	}
	if f.set["app"] {
		if serr := client.SetPaneOption(pane, "@spaces_app", f.str["app"]); serr != nil {
			env.errf("tmux-spaces status: %s\n", serr)
			return ExitError
		}
	}
	if f.set["profile"] {
		if f.str["profile"] == "" {
			if uerr := client.UnsetPaneOption(pane, "@spaces_profile"); uerr != nil {
				env.errf("tmux-spaces status: %s\n", uerr)
				return ExitError
			}
		} else if serr := client.SetPaneOption(pane, "@spaces_profile", f.str["profile"]); serr != nil {
			env.errf("tmux-spaces status: %s\n", serr)
			return ExitError
		}
	}
	if f.set["kind"] {
		if f.str["kind"] == "" {
			if uerr := client.UnsetPaneOption(pane, "@spaces_kind"); uerr != nil {
				env.errf("tmux-spaces status: %s\n", uerr)
				return ExitError
			}
		} else if serr := client.SetPaneOption(pane, "@spaces_kind", f.str["kind"]); serr != nil {
			env.errf("tmux-spaces status: %s\n", serr)
			return ExitError
		}
	}
	if f.set["msg"] {
		if f.str["msg"] == "" {
			if uerr := client.UnsetPaneOption(pane, "@spaces_msg"); uerr != nil {
				env.errf("tmux-spaces status: %s\n", uerr)
				return ExitError
			}
		} else if serr := client.SetPaneOption(pane, "@spaces_msg", f.str["msg"]); serr != nil {
			env.errf("tmux-spaces status: %s\n", serr)
			return ExitError
		}
	}
	if serr := client.SetPaneOption(pane, "@spaces_ts", itoa(int(env.Now().UnixMilli()))); serr != nil {
		env.errf("tmux-spaces status: %s\n", serr)
		return ExitError
	}
	return ExitOK
}
