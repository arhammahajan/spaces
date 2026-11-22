package cli

import (
	"os"
	"strings"
)

// cmdSpace dispatches `space|spaces [list|current|new|switch|rename|close]`.
func cmdSpace(args []string, env Env) int {
	sub := "list"
	rest := []string{}
	if len(args) > 0 {
		sub = args[0]
		rest = args[1:]
	}
	switch sub {
	case "list", "ls":
		return cmdSpaceList(rest, env)
	case "current":
		return cmdSpaceCurrent(rest, env)
	case "new":
		return cmdSpaceNew(rest, env)
	case "switch", "open":
		return cmdSpaceSwitch(rest, env)
	case "rename":
		return cmdSpaceRename(rest, env)
	case "close", "kill":
		return cmdSpaceClose(rest, env, sub)
	case "-h", "--help":
		env.outf("usage: tmux-spaces space [list|current|new|switch|rename|close] ...\n")
		return ExitOK
	default:
		env.errf("tmux-spaces space: unknown subcommand %q\n", sub)
		return ExitUsage
	}
}

func currentSessionName(env Env, client TmuxClient) (string, bool) {
	if name, ok := client.CurrentSession(); ok && name != "" {
		return name, true
	}
	return "", false
}

func cmdSpaceList(args []string, env Env) int {
	if len(args) > 0 {
		env.errf("usage: tmux-spaces space list\n")
		return ExitUsage
	}
	client, err := env.requireClient()
	if err != nil {
		env.errf("tmux-spaces space list: %s\n", err)
		return ExitError
	}
	sessions, err := client.ListSessions()
	if err != nil {
		env.errf("tmux-spaces space list: %s\n", err)
		return ExitError
	}
	current, _ := currentSessionName(env, client)
	env.outf("  %-22s %4s %5s %6s %5s %5s  %s\n",
		"SPACE", "WIN", "NEEDS", "FAILED", "WORK", "DONE", "CWD")
	for _, s := range sessions {
		windows, werr := client.ListWindows(s.Name)
		panes, perr := client.ListPanes(s.Name)
		if werr != nil || perr != nil {
			continue
		}
		var needs, failed, working, done int
		cwd := ""
		for _, p := range panes {
			switch bucketOf(p.SpacesState) {
			case BucketNeeds:
				needs++
			case BucketFailed:
				failed++
			case BucketWorking:
				working++
			case BucketDone:
				done++
			}
			if cwd == "" && p.Active && p.CurrentPath != "" {
				cwd = p.CurrentPath
			}
		}
		marker := " "
		if s.Name == current {
			marker = "*"
		}
		env.outf("%s %-22s %4d %5d %6d %5d %5d  %s\n",
			marker, s.Name, len(windows), needs, failed, working, done, cwd)
	}
	return ExitOK
}

func cmdSpaceCurrent(args []string, env Env) int {
	if len(args) > 0 {
		env.errf("usage: tmux-spaces space current\n")
		return ExitUsage
	}
	client, err := env.requireClient()
	if err != nil {
		env.errf("tmux-spaces space current: %s\n", err)
		return ExitError
	}
	if name, ok := currentSessionName(env, client); ok {
		env.outf("%s\n", name)
		return ExitOK
	}
	env.errf("tmux-spaces space current: not inside tmux and no unambiguous session\n")
	return ExitError
}

func validSpaceName(name string) bool {
	if name == "" {
		return false
	}
	return !strings.ContainsAny(name, ":.")
}

func cmdSpaceNew(args []string, env Env) int {
	client, err := env.requireClient()
	if err != nil {
		env.errf("tmux-spaces space new: %s\n", err)
		return ExitError
	}
	f, ferr := parseFlags(args, nil, nil, nil)
	if ferr != nil {
		if ferr == errHelp {
			env.outf("usage: tmux-spaces space new <name> [cwd]\n")
			return ExitOK
		}
		env.errf("tmux-spaces space new: %s\n", ferr)
		return ExitUsage
	}
	if len(f.pos) < 1 || len(f.pos) > 2 {
		env.errf("usage: tmux-spaces space new <name> [cwd]\n")
		return ExitUsage
	}
	name := f.pos[0]
	if !validSpaceName(name) {
		env.errf("tmux-spaces space new: invalid name %q (must not contain ':' or '.')\n", name)
		return ExitUsage
	}
	cwd := ""
	if len(f.pos) > 1 {
		cwd = f.pos[1]
	} else if pwd := env.Getenv("PWD"); pwd != "" {
		cwd = pwd
	} else if wd, werr := os.Getwd(); werr == nil {
		cwd = wd
	} else {
		cwd = "."
	}
	if fi, serr := os.Stat(cwd); serr != nil || !fi.IsDir() {
		env.errf("tmux-spaces space new: not a directory: %s\n", cwd)
		return ExitError
	}
	for _, s := range listSessionNames(client) {
		if s == name {
			env.errf("tmux-spaces space new: space %q already exists\n", name)
			return ExitError
		}
	}
	if err := client.NewSession(name, cwd); err != nil {
		env.errf("tmux-spaces space new: %s\n", err)
		return ExitError
	}
	env.outf("%s\n", name)
	return ExitOK
}

func listSessionNames(client TmuxClient) []string {
	sessions, err := client.ListSessions()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(sessions))
	for _, s := range sessions {
		names = append(names, s.Name)
	}
	return names
}

func cmdSpaceSwitch(args []string, env Env) int {
	client, err := env.requireClient()
	if err != nil {
		env.errf("tmux-spaces space switch: %s\n", err)
		return ExitError
	}
	f, ferr := parseFlags(args, nil, nil, nil)
	if ferr != nil {
		if ferr == errHelp {
			env.outf("usage: tmux-spaces space switch <name>\n")
			return ExitOK
		}
		env.errf("tmux-spaces space switch: %s\n", ferr)
		return ExitUsage
	}
	if len(f.pos) != 1 {
		env.errf("usage: tmux-spaces space switch <name>\n")
		return ExitUsage
	}
	name := f.pos[0]
	found := false
	for _, s := range listSessionNames(client) {
		if s == name {
			found = true
			break
		}
	}
	if !found {
		env.errf("tmux-spaces space switch: no space named %q\n", name)
		return ExitError
	}
	if err := client.SwitchTo(name); err != nil {
		env.errf("tmux-spaces space switch: %s\n", err)
		return ExitError
	}
	return ExitOK
}

func cmdSpaceRename(args []string, env Env) int {
	client, err := env.requireClient()
	if err != nil {
		env.errf("tmux-spaces space rename: %s\n", err)
		return ExitError
	}
	f, ferr := parseFlags(args, nil, nil, nil)
	if ferr != nil {
		if ferr == errHelp {
			env.outf("usage: tmux-spaces space rename <new> [old]\n")
			return ExitOK
		}
		env.errf("tmux-spaces space rename: %s\n", ferr)
		return ExitUsage
	}
	if len(f.pos) < 1 || len(f.pos) > 2 {
		env.errf("usage: tmux-spaces space rename <new> [old]\n")
		return ExitUsage
	}
	newName := f.pos[0]
	if !validSpaceName(newName) {
		env.errf("tmux-spaces space rename: invalid name %q (must not contain ':' or '.')\n", newName)
		return ExitUsage
	}
	oldName := ""
	if len(f.pos) > 1 {
		oldName = f.pos[1]
	} else if cur, ok := currentSessionName(env, client); ok {
		oldName = cur
	} else {
		env.errf("tmux-spaces space rename: no current space; pass <new> [old]\n")
		return ExitError
	}
	if err := client.RenameSession(oldName, newName); err != nil {
		env.errf("tmux-spaces space rename: %s\n", err)
		return ExitError
	}
	env.outf("%s\n", newName)
	return ExitOK
}

func cmdSpaceClose(args []string, env Env, sub string) int {
	client, err := env.requireClient()
	if err != nil {
		env.errf("tmux-spaces space %s: %s\n", sub, err)
		return ExitError
	}
	f, ferr := parseFlags(args, nil, []string{"yes"}, map[string]string{"y": "yes"})
	if ferr != nil {
		if ferr == errHelp {
			env.outf("usage: tmux-spaces space %s <name> [--yes]\n", sub)
			return ExitOK
		}
		env.errf("tmux-spaces space %s: %s\n", sub, ferr)
		return ExitUsage
	}
	name := ""
	if len(f.pos) > 1 {
		env.errf("usage: tmux-spaces space %s [name] [--yes]\n", sub)
		return ExitUsage
	}
	if len(f.pos) == 1 {
		name = f.pos[0]
	} else if cur, ok := currentSessionName(env, client); ok {
		name = cur
	} else {
		env.errf("tmux-spaces space %s: no current space; pass a name\n", sub)
		return ExitError
	}
	if !f.set["yes"] {
		if stdinIsTerminal(env) {
			env.errf("Close space %q? [y/N] ", name)
			var answer [256]byte
			n, _ := env.Stdin.Read(answer[:])
			resp := strings.ToLower(strings.TrimSpace(string(answer[:n])))
			if resp != "y" && resp != "yes" {
				env.errf("cancelled\n")
				return ExitOK
			}
		} else {
			env.errf("tmux-spaces space %s: refusing to close %q without --yes\n", sub, name)
			return ExitError
		}
	}
	if err := client.KillSession(name); err != nil {
		env.errf("tmux-spaces space %s: %s\n", sub, err)
		return ExitError
	}
	for _, s := range listSessionNames(client) {
		if s == name {
			env.errf("tmux-spaces space %s: could not close space %q\n", sub, name)
			return ExitError
		}
	}
	env.errf("tmux-spaces: closed space %s\n", name)
	return ExitOK
}

// stdinIsTerminal reports whether env Stdin (or os.Stdin) is a character
// device, i.e. an interactive terminal worth prompting on.
func stdinIsTerminal(env Env) bool {
	if f, ok := env.Stdin.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return true
		}
		return false
	}
	if env.Stdin != nil {
		return false
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return true
	}
	return false
}
