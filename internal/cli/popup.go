package cli

// cmdPopup opens the Navigator TUI. The TUI itself is wired by
// cmd/tmux-spaces through Env.RunPopup; without it the command reports
// that instead of failing obscurely.
func cmdPopup(args []string, env Env) int {
	f, err := parseFlags(args, []string{"query", "target"}, []string{"new", "rename-window", "rename-space"}, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces popup [--new] [--rename-window] [--rename-space] [--query Q] [--target SPACE]\n")
			return ExitOK
		}
		env.errf("tmux-spaces popup: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces popup [--new] [--rename-window] [--rename-space] [--query Q] [--target SPACE]\n")
		return ExitUsage
	}
	if env.RunPopup == nil {
		env.errf("tmux-spaces popup: TUI not wired (cmd/tmux-spaces must provide RunPopup)\n")
		return ExitError
	}
	return env.RunPopup(env, args)
}

// cmdAgentsCMD opens the Agents selector TUI, wired the same way as popup.
func cmdAgentsCMD(args []string, env Env) int {
	f, err := parseFlags(args, nil, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces agents\n")
			return ExitOK
		}
		env.errf("tmux-spaces agents: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces agents\n")
		return ExitUsage
	}
	if env.RunAgents == nil {
		env.errf("tmux-spaces agents: TUI not wired (cmd/tmux-spaces must provide RunAgents)\n")
		return ExitError
	}
	return env.RunAgents(env, args)
}

// cmdInbox opens the Attention Inbox TUI, showing only agents across all
// spaces with a compact live viewport and status.
func cmdInbox(args []string, env Env) int {
	f, err := parseFlags(args, nil, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces inbox\n")
			return ExitOK
		}
		env.errf("tmux-spaces inbox: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces inbox\n")
		return ExitUsage
	}
	if env.RunInbox == nil {
		env.errf("tmux-spaces inbox: TUI not wired (cmd/tmux-spaces must provide RunInbox)\n")
		return ExitError
	}
	return env.RunInbox(env, args)
}

// cmdConversations opens the unified Conversations search/resume TUI.
func cmdConversations(args []string, env Env) int {
	f, err := parseFlags(args, []string{"query"}, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces conversations [--query Q]\n")
			return ExitOK
		}
		env.errf("tmux-spaces conversations: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces conversations [--query Q]\n")
		return ExitUsage
	}
	if env.RunConversations == nil {
		env.errf("tmux-spaces conversations: TUI not wired (cmd/tmux-spaces must provide RunConversations)\n")
		return ExitError
	}
	return env.RunConversations(env, args)
}

// cmdShared opens the Shared MCP & Skills browser TUI.
func cmdShared(args []string, env Env) int {
	f, err := parseFlags(args, []string{"dir", "query"}, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces shared [--dir DIR] [--query Q]\n")
			return ExitOK
		}
		env.errf("tmux-spaces shared: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces shared [--dir DIR] [--query Q]\n")
		return ExitUsage
	}
	if env.RunShared == nil {
		env.errf("tmux-spaces shared: TUI not wired (cmd/tmux-spaces must provide RunShared)\n")
		return ExitError
	}
	return env.RunShared(env, args)
}

// cmdTabs opens the current-workspace Tabs inspector & switcher TUI.
func cmdTabs(args []string, env Env) int {
	f, err := parseFlags(args, []string{"session", "query"}, []string{"new"}, map[string]string{"s": "session", "q": "query"})
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces tabs [--new] [--session SPACE] [--query Q]\n")
			return ExitOK
		}
		env.errf("tmux-spaces tabs: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces tabs [--new] [--session SPACE] [--query Q]\n")
		return ExitUsage
	}
	if env.RunTabs == nil {
		env.errf("tmux-spaces tabs: TUI not wired (cmd/tmux-spaces must provide RunTabs)\n")
		return ExitError
	}
	return env.RunTabs(env, args)
}

// cmdUsage opens the Usage & Burndown popup TUI.
func cmdUsage(args []string, env Env) int {
	f, err := parseFlags(args, nil, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces usage\n")
			return ExitOK
		}
		env.errf("tmux-spaces usage: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces usage\n")
		return ExitUsage
	}
	if env.RunUsage == nil {
		env.errf("tmux-spaces usage: TUI not wired (cmd/tmux-spaces must provide RunUsage)\n")
		return ExitError
	}
	return env.RunUsage(env, args)
}
