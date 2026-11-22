package cli

import (
	"strings"
)

// cmdPrompt sends literal text + Enter to a tmux pane/window/session.
func cmdPrompt(args []string, env Env) int {
	// Split off nothing: every positional after the target is message text.
	// Flags are not supported here so that messages starting with dashes
	// pass through untouched.
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		env.outf("usage: tmux-spaces prompt <target> <text...>\n")
		return ExitOK
	}
	if len(args) < 2 {
		env.errf("usage: tmux-spaces prompt <target> <text...>\n")
		return ExitUsage
	}
	client, err := env.requireClient()
	if err != nil {
		env.errf("tmux-spaces prompt: %s\n", err)
		return ExitError
	}
	target := args[0]
	text := strings.Join(args[1:], " ")
	if strings.TrimSpace(text) == "" {
		env.errf("usage: tmux-spaces prompt <target> <text...>\n")
		return ExitUsage
	}
	if err := client.SendKeys(target, text); err != nil {
		env.errf("tmux-spaces prompt: %s\n", err)
		return ExitError
	}
	return ExitOK
}

// cmdRead captures a pane viewport via CapturePane and prints it.
func cmdRead(args []string, env Env) int {
	f, err := parseFlags(args, []string{"lines"}, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces read <target> [--lines N]\n")
			return ExitOK
		}
		env.errf("tmux-spaces read: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) != 1 {
		env.errf("usage: tmux-spaces read <target> [--lines N]\n")
		return ExitUsage
	}
	lines := 200
	if f.set["lines"] {
		n, ok := atoi(f.str["lines"])
		if !ok || n <= 0 {
			env.errf("tmux-spaces read: --lines must be a positive number\n")
			return ExitUsage
		}
		lines = n
	}
	client, cerr := env.requireClient()
	if cerr != nil {
		env.errf("tmux-spaces read: %s\n", cerr)
		return ExitError
	}
	text, rerr := client.CapturePane(f.pos[0], lines)
	if rerr != nil {
		env.errf("tmux-spaces read: %s\n", rerr)
		return ExitError
	}
	if text == "" {
		return ExitOK
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	env.outf("%s", text)
	return ExitOK
}

func atoi(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}
