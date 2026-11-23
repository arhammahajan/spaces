package cli

import (
	"os"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

// cmdAgent opens an agent in a new tmux window under `__shim`. With
// --profile the pane-side launch is wrapped by __shim so tmux status reporting
// behaves identically.
func cmdAgent(args []string, env Env) int {
	// Split extra agent args at "--" before flag parsing so values with
	// leading dashes pass through untouched.
	extra := []string{}
	head := args
	for i, a := range args {
		if a == "--" {
			head, extra = args[:i], args[i+1:]
			break
		}
	}
	f, err := parseFlags(head, []string{"profile", "session", "cwd"}, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces agent <name> [--profile P] [--session S] [--cwd D] [-- args...]\n")
			return ExitOK
		}
		env.errf("tmux-spaces agent: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) != 1 {
		env.errf("usage: tmux-spaces agent <name> [--profile P] [--session S] [--cwd D] [-- args...]\n")
		return ExitUsage
	}
	name := f.pos[0]
	if strings.TrimSpace(name) == "" {
		env.errf("usage: tmux-spaces agent <name> [--profile P] [--session S] [--cwd D] [-- args...]\n")
		return ExitUsage
	}
	client, cerr := env.requireClient()
	if cerr != nil {
		env.errf("tmux-spaces agent: %s\n", cerr)
		return ExitError
	}
	session := f.str["session"]
	if session == "" {
		cur, ok := currentSessionName(env, client)
		if !ok {
			env.errf("tmux-spaces agent: no current session (pass --session)\n")
			return ExitError
		}
		session = cur
	}
	cwd := f.str["cwd"]
	if cwd == "" {
		if pwd := env.Getenv("PWD"); pwd != "" {
			cwd = pwd
		} else if wd, werr := os.Getwd(); werr == nil {
			cwd = wd
		} else {
			cwd = "."
		}
	}
	if fi, serr := os.Stat(cwd); serr != nil || !fi.IsDir() {
		env.errf("tmux-spaces agent: not a directory: %s\n", cwd)
		return ExitError
	}
	profile := f.str["profile"]
	label := name
	var cmd []string
	if profile != "" {
		if profile != "default" {
			label = profile
		}
		cmd = []string{env.Self, "__shim", "--profile", profile, canonicalApp(name), env.Self}
		cmd = append(cmd, agent.HarnessProfileCommand(name, profile, extra...)...)
	} else {
		bin := agent.DefaultRegistry().BinaryName(name)
		if _, lerr := env.LookPath(bin); lerr != nil {
			env.errf("tmux-spaces agent: agent %q not found on PATH\n", bin)
			return ExitError
		}
		cmd = append([]string{env.Self, "__shim", canonicalApp(name), bin}, extra...)
	}
	if err := client.NewWindow(session, cwd, label, cmd); err != nil {
		env.errf("tmux-spaces agent: %s\n", err)
		return ExitError
	}
	return ExitOK
}
