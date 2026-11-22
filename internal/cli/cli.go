// Package cli implements the tmux-spaces command surface as a library with
// injected seams, so it compiles and tests without tmux, the TUI, or the
// harness tree.
//
// cmd/tmux-spaces wires a *tmux.Client (directly when its method set
// matches TmuxClient, otherwise through a thin adapter) plus the Bubbletea
// TUI callbacks into Env and calls Run.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	_ "github.com/arhammahajan/spaces/internal/agent/plugins"
	"github.com/arhammahajan/spaces/internal/tmux"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// TmuxClient is the tmux surface the CLI needs. It mirrors the capabilities
// of internal/tmux (snapshot/list, per-pane options, send-keys,
// capture-pane, and session management) so cmd/tmux-spaces can pass
// *tmux.Client directly when its method set matches, or through a thin
// adapter. Tests substitute a fake.
type TmuxClient interface {
	BinaryPath() string
	ServerRunning() bool
	CurrentSession() (string, bool)
	Snapshot() (tmux.ServerSnapshot, error)
	ListSessions() ([]tmux.Session, error)
	ListWindows(session string) ([]tmux.Window, error)
	ListPanes(session string) ([]tmux.Pane, error)
	SetPaneOption(pane, option, value string) error
	UnsetPaneOption(pane, option string) error
	// SendKeys types text literally into target, then presses Enter.
	SendKeys(target, text string) error
	CapturePane(target string, lines int) (string, error)
	// SwitchTo attaches or switches the client to a session, window, or
	// pane target (e.g. "name" or "name:@1.%2").
	SwitchTo(target string) error
	NewSession(name, cwd string) error
	RenameSession(oldName, newName string) error
	KillSession(name string) error
	NewWindow(session, cwd, label string, cmd []string) error
}

// Env carries every external interaction of the CLI. Zero values are filled
// with OS-backed defaults by Run; tests inject fakes.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Now    func() time.Time
	Sleep  func(time.Duration)

	// Client is the tmux backend. Required by every command except help.
	Client TmuxClient

	Getenv   func(string) string
	LookPath func(string) (string, error)
	// Self is the binary path used when spawning subcommands
	// (agent builds a `__shim` command line from it).
	Self string

	// LoadSpaces supplies the server snapshot for read-only views.
	// Defaults to Client.Snapshot.
	LoadSpaces func() (tmux.ServerSnapshot, error)
	// RunPopup launches the Navigator TUI (wired by cmd/tmux-spaces).
	RunPopup func(Env, []string) int
	// RunAgents launches the Agents selector TUI (wired by cmd/tmux-spaces).
	RunAgents func(Env, []string) int
	// RunInbox launches the Attention Inbox TUI (wired by cmd/tmux-spaces).
	RunInbox func(Env, []string) int
	// RunConversations launches the Conversations search/resume TUI (wired by cmd/tmux-spaces).
	RunConversations func(Env, []string) int
	// RunShared launches the Shared MCP & Skills TUI (wired by cmd/tmux-spaces).
	RunShared func(Env, []string) int
	// RunTabs launches the Current-Workspace Tabs TUI (wired by cmd/tmux-spaces).
	RunTabs func(Env, []string) int
	// RunChild runs the __shim child process, returning its exit code.
	RunChild func(bin string, args []string) int
	// RunShell runs the interactive shell for __shim --hold.
	RunShell func() int
	// HookHealth reports hook listener health for doctor.
	// An empty string means unknown.
	HookHealth func() (string, error)
	// RunResurrect handles `tmux-spaces resurrect ...` (wired by cmd/tmux-spaces).
	RunResurrect func(Env, []string) int
	// RunWorktrees handles `tmux-spaces worktrees|wt ...` (wired by cmd/tmux-spaces).
	RunWorktrees func(Env, []string) int
	// MaybeAutoSave performs a throttled background snapshot save (wired by cmd/tmux-spaces).
	MaybeAutoSave func()
	// RecordOutcome persists a recent completion/failure entry (wired by cmd/tmux-spaces).
	RecordOutcome func(OutcomeRecord)
}

// OutcomeRecord carries one recent completion/failure event from __shim or __osc-watch.
type OutcomeRecord struct {
	Session string
	Pane    string
	App     string
	Profile string
	Outcome string // "done" or "failed"
	Text    string
	CWD     string
	TsMs    int64
}

// DefaultEnv returns an Env with OS-backed defaults around client.
func DefaultEnv(client TmuxClient) Env {
	return Env{Client: client}.withDefaults()
}

func (e Env) withDefaults() Env {
	if e.Stdout == nil {
		e.Stdout = os.Stdout
	}
	if e.Stderr == nil {
		e.Stderr = os.Stderr
	}
	if e.Stdin == nil {
		e.Stdin = os.Stdin
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Sleep == nil {
		e.Sleep = time.Sleep
	}
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	if e.LookPath == nil {
		e.LookPath = exec.LookPath
	}
	if e.Self == "" {
		e.Self = selfBinary()
	}
	if e.LoadSpaces == nil && e.Client != nil {
		client := e.Client
		e.LoadSpaces = client.Snapshot
	}
	if e.RunChild == nil {
		e.RunChild = runChildReal
	}
	if e.RunShell == nil {
		e.RunShell = runShellReal
	}
	return e
}

func selfBinary() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return "tmux-spaces"
}

func runChildReal(bin string, args []string) int {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return ExitError
	}
	return ExitOK
}

func runShellReal() int {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-l")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return ExitError
	}
	return ExitOK
}

func (e Env) outf(format string, args ...any) {
	fmt.Fprintf(e.Stdout, format, args...)
}

func (e Env) errf(format string, args ...any) {
	fmt.Fprintf(e.Stderr, format, args...)
}

func (e Env) loadSpaces() (tmux.ServerSnapshot, error) {
	if e.LoadSpaces == nil {
		return tmux.ServerSnapshot{}, errors.New("no tmux client")
	}
	return e.LoadSpaces()
}

func (e Env) requireClient() (TmuxClient, error) {
	if e.Client == nil {
		return nil, errors.New("no tmux client")
	}
	return e.Client, nil
}

// Status buckets for @spaces_state values.
const (
	BucketNeeds   = "needs"
	BucketFailed  = "failed"
	BucketWorking = "working"
	BucketDone    = "done"
	BucketIdle    = "idle"
)

// bucketOf maps a raw @spaces_state value onto a coarse bucket.
// Unknown and empty values are idle.
func bucketOf(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "blocked", "needs_you", "needs-you", "needsyou":
		return BucketNeeds
	case "failed", "error":
		return BucketFailed
	case "done":
		return BucketDone
	case "working":
		return BucketWorking
	default:
		return BucketIdle
	}
}

// bucketPriority orders buckets for attention ranking (lower is hotter).
func bucketPriority(bucket string) int {
	switch bucket {
	case BucketNeeds:
		return 0
	case BucketFailed:
		return 1
	case BucketDone:
		return 2
	case BucketWorking:
		return 3
	default:
		return 4
	}
}

// canonicalApp mirrors the harness canonical app labels.
func canonicalApp(app string) string {
	return agent.DefaultRegistry().CLIDisplayApp(app)
}

// errHelp is returned by parseFlags when -h/--help is present.
var errHelp = errors.New("help requested")

type parsedFlags struct {
	str map[string]string
	set map[string]bool
	pos []string
}

// parseFlags parses GNU-style flags out of args. strFlags take a value
// (--flag v or --flag=v), boolFlags are switches, aliases maps short forms
// (e.g. "-y") to canonical names. A "--" element ends flag parsing. Unknown
// flags and missing values are errors; -h/--help returns errHelp.
func parseFlags(args []string, strFlags []string, boolFlags []string, aliases map[string]string) (parsedFlags, error) {
	isStr := map[string]bool{}
	for _, f := range strFlags {
		isStr[f] = true
	}
	isBool := map[string]bool{}
	for _, f := range boolFlags {
		isBool[f] = true
	}
	out := parsedFlags{str: map[string]string{}, set: map[string]bool{}}
	onlyPos := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if onlyPos {
			out.pos = append(out.pos, a)
			continue
		}
		if a == "--" {
			onlyPos = true
			continue
		}
		if len(a) < 2 || a[0] != '-' {
			out.pos = append(out.pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		value := ""
		hasValue := false
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			value, hasValue = name[idx+1:], true
			name = name[:idx]
		}
		if target, ok := aliases[name]; ok {
			name = target
		}
		if name == "h" || name == "help" {
			return out, errHelp
		}
		switch {
		case isBool[name]:
			if hasValue {
				return out, fmt.Errorf("flag --%s takes no value", name)
			}
			out.set[name] = true
		case isStr[name]:
			if !hasValue {
				if i+1 >= len(args) {
					return out, fmt.Errorf("flag --%s needs a value", name)
				}
				i++
				value = args[i]
			}
			out.str[name] = value
			out.set[name] = true
		default:
			return out, fmt.Errorf("unknown flag --%s", name)
		}
	}
	return out, nil
}

// Run dispatches argv (without argv[0]) and returns the exit code.
func Run(args []string, env Env) int {
	env = env.withDefaults()
	if len(args) == 0 {
		printTopHelp(env)
		return ExitOK
	}
	cmd, rest := args[0], args[1:]
	if cmd == "-h" || cmd == "--help" {
		printTopHelp(env)
		return ExitOK
	}
	switch cmd {
	case "status-bar":
		return cmdStatusBar(rest, env)
	case "space", "spaces":
		return cmdSpace(rest, env)
	case "panes":
		return cmdPanes(rest, env)
	case "status":
		return cmdStatus(rest, env)
	case "prompt":
		return cmdPrompt(rest, env)
	case "read":
		return cmdRead(rest, env)
	case "wait":
		return cmdWait(rest, env)
	case "next":
		return cmdNext(rest, env)
	case "agent":
		return cmdAgent(rest, env)
	case "__shim":
		return cmdShim(rest, env)
	case "__osc-watch":
		return cmdOSCWatch(rest, env)
	case "doctor":
		return cmdDoctor(rest, env)
	case "popup":
		return cmdPopup(rest, env)
	case "agents":
		return cmdAgentsCMD(rest, env)
	case "inbox":
		return cmdInbox(rest, env)
	case "conversations", "chats":
		return cmdConversations(rest, env)
	case "shared", "skills", "mcp":
		return cmdShared(rest, env)
	case "tabs", "tab":
		return cmdTabs(rest, env)
	case "resurrect":
		if env.RunResurrect == nil {
			env.errf("tmux-spaces resurrect: not wired\n")
			return ExitError
		}
		return env.RunResurrect(env, rest)
	case "worktrees", "worktree", "wt":
		if env.RunWorktrees == nil {
			env.errf("tmux-spaces worktrees: not wired\n")
			return ExitError
		}
		return env.RunWorktrees(env, rest)
	case "help":
		return cmdHelp(rest, env)
	default:
		env.errf("tmux-spaces: unknown command %q (see 'tmux-spaces help')\n", cmd)
		return ExitUsage
	}
}

func printTopHelp(env Env) {
	env.outf(`tmux-spaces - tmux-native agent spaces

usage: tmux-spaces <command> [args]

  status-bar [--plain] [--json]   status-line badge segment
  space|spaces ...                 manage spaces (tmux sessions)
  panes [--session S] [--json]     list panes with status
  status STATE [flags]             set @spaces_* on a pane
  prompt TARGET TEXT...            type text + Enter into a pane
  read TARGET [--lines N]          capture a pane viewport
  wait TARGET [flags]              wait for a pane state
  next                             jump to the hottest attention pane
  agent NAME [flags] [-- args...]  open an agent in a new window
  __shim [--hold] APP BIN [args]   pane-side agent wrapper
  doctor [--json]                  check tmux/server/hooks health
  popup [--new]                    open the Navigator TUI
  tabs|tab [--new] [--session S]   inspect & switch tabs in current workspace
  worktrees|wt [list|new|remove|prune]
                                   manage git worktrees (or open Worktrees TUI)
  agents                           open the Agents selector TUI
  inbox                            open the Attention Inbox TUI
  conversations|chats [--query Q]  search & resume agent conversations
  shared|skills|mcp [--dir D]      browse shared MCP servers and skills
  tabs|tab [--new] [--session S]   inspect & switch tabs in current workspace
  help [command]                   show help
`)
}

func cmdHelp(args []string, env Env) int {
	if len(args) == 0 {
		printTopHelp(env)
		return ExitOK
	}
	switch args[0] {
	case "status-bar":
		env.outf("usage: tmux-spaces status-bar [--plain] [--json]\n")
	case "space", "spaces":
		env.outf("usage: tmux-spaces space [list|current|new|switch|rename|close] ...\n")
	case "panes":
		env.outf("usage: tmux-spaces panes [--session S] [--json]\n")
	case "status":
		env.outf("usage: tmux-spaces status <idle|working|done|blocked|error|clear> [--app A] [--kind K] [--msg M] [--pane %%N]\n")
	case "prompt":
		env.outf("usage: tmux-spaces prompt <target> <text...>\n")
	case "read":
		env.outf("usage: tmux-spaces read <target> [--lines N]\n")
	case "wait":
		env.outf("usage: tmux-spaces wait <target> [--until STATE] [--timeout DURATION]\n")
	case "next":
		env.outf("usage: tmux-spaces next\n")
	case "agent":
		env.outf("usage: tmux-spaces agent <name> [--profile P] [--session S] [--cwd D] [-- args...]\n")
	case "__shim":
		env.outf("usage: tmux-spaces __shim [--hold] <app> <bin> [args...]\n")
	case "doctor":
		env.outf("usage: tmux-spaces doctor [--json]\n")
	case "popup":
		env.outf("usage: tmux-spaces popup [--new] [--new-tab] [--rename-window] [--rename-space] [--query Q] [--target SPACE]\n")
	case "worktrees", "worktree", "wt":
		env.outf("usage: tmux-spaces worktrees [list|new|remove|prune] ...\n")
	case "agents":
		env.outf("usage: tmux-spaces agents\n")
	case "inbox":
		env.outf("usage: tmux-spaces inbox\n")
	case "conversations", "chats":
		env.outf("usage: tmux-spaces conversations [--query Q]\n")
	case "shared", "skills", "mcp":
		env.outf("usage: tmux-spaces shared [--dir DIR] [--query Q]\n")
	case "tabs", "tab":
		env.outf("usage: tmux-spaces tabs [--new] [--session SPACE] [--query Q]\n")
	default:
		env.errf("tmux-spaces: unknown command %q\n", args[0])
		return ExitUsage
	}
	return ExitOK
}
