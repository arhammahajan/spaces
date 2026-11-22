// Package client implements the hook client CLI surface for tmux-spaces:
// stdin JSON payload reading, OSC 7501 & native tmux @spaces_* status
// emission, publish-or-spool transport, and the hooks/hook-server/__space-hooks
// command entries.
//
// It consumes the hook-core backend and title discovery through package-level
// injectable seam variables so cmd/tmux-spaces can wire them without creating
// circular imports. All seam call sites are nil-safe and degrade gracefully.
package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

// Injectable seams wired at startup by cmd/tmux-spaces. All call sites are
// nil-safe and fall back to degraded behavior when unset.
var (
	// HookDisabled reports whether the hook listener is disabled.
	HookDisabled func() bool
	// NormalizeEvent builds the normalized hook envelope.
	NormalizeEvent func(agent, event string, payload map[string]any, env map[string]string) map[string]any
	// ResolvePaneEvent resolves a normalized event against the pane's stored
	// state (e.g. active background work) before emitting tmux/OSC status.
	ResolvePaneEvent func(session, pane string, normalized map[string]any) map[string]any
	// Publish posts an envelope to the hook listener. False means spool it.
	Publish func(envelope map[string]any) bool
	// AppendSpool appends an entry to the hook spool file.
	AppendSpool func(envelope map[string]any) error
	// EnsureServer lazily starts the hook listener.
	EnsureServer func(helperPath string) bool
	// SpaceHooksSummary returns the spaces summary view.
	SpaceHooksSummary func() map[string]any
	// SpaceHooksDetail returns the per-pane spaces detail view.
	SpaceHooksDetail func(session string, pane any) map[string]any
	// RecordOutcome persists one recent completion/failure entry into the hook store.
	RecordOutcome func(entry map[string]any) error
	// RemoveOutcome deletes matching recent outcome entries from the hook store.
	RemoveOutcome func(match map[string]any) error
	// PaneState returns the current non-stale state for (session, pane), or "".
	PaneState func(session string, pane any) string
	// HooksStatus implements `hooks status`.
	HooksStatus func() int
	// HooksStop implements `hooks stop`.
	HooksStop func() int
	// HooksStart implements `hooks start|restart`.
	HooksStart func(helperPath string) int
	// Daemonize implements `hook-server --daemon`.
	Daemonize func() int
	// Serve runs the hook listener in the foreground.
	Serve func(foreground bool, tcpPort *int) int
	// DiscoverTitleFunc enriches hook events and outcomes with conversation
	// titles (nil-safe; wired by cmd/tmux-spaces to model.FetchConversationTitles).
	DiscoverTitleFunc func(agent, cwd, sessionID string) string
)

// Injectable seams for execution, environment observation, and output.
// Tests override these; production defaults shell out or read the OS.
var (
	// RunBinFunc runs an arbitrary helper binary (tmux, harness).
	RunBinFunc func(name string, args []string, cwd string, timeout time.Duration) (string, int, error)
	// LookPathFunc resolves a binary on PATH.
	LookPathFunc func(file string) (string, error)
	// AncestorsFunc returns process ancestor command lines, nearest first.
	AncestorsFunc func() [][]string
	// TtyPathsFunc returns writable tty paths for OSC emission, in order.
	TtyPathsFunc func() []string
	// StdinValue overrides os.Stdin for hook payload reads (nil = os.Stdin).
	StdinValue interface {
		Read([]byte) (int, error)
	}
	// Stdout receives data payloads (JSON). Defaults to os.Stdout.
	Stdout *os.File
	// Stderr receives status/usage lines. Defaults to os.Stderr.
	Stderr *os.File
	// NowFunc reports the current time.
	NowFunc func() time.Time
)

func init() {
	RunBinFunc = realRunBin
	LookPathFunc = exec.LookPath
	AncestorsFunc = processAncestorCommands
	TtyPathsFunc = reportTTYPaths
	NowFunc = time.Now
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = time.Second
	}
	return context.WithTimeout(context.Background(), d)
}

func realRunBin(name string, args []string, cwd string, timeout time.Duration) (string, int, error) {
	ctx, cancel := contextWithTimeout(timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return string(out), exitErr.ExitCode(), nil
		}
		return string(out), 0, err
	}
	return string(out), 0, nil
}

// Aliases maps shorthand agent names to canonical agents when the plugin
// registry does not have plugins registered (e.g. isolated unit tests).
var Aliases = map[string]string{
	"agy":         "antigravity",
	"claude-code": "claude",
	"dsh":         "deepseek",
	"kimi-code":   "kimi",
	"cursor":      "cursor-agent",
}

// AppNames maps canonical agents to the app label they use in OSC records
// when the plugin registry does not have plugins registered.
var AppNames = map[string]string{
	"claude":   "claude-code",
	"kimi-cli": "kimi",
	"dsh":      "deepseek",
	"dsh-tui":  "deepseek",
	"cursor":   "cursor-agent",
}

// Agents is the fallback set of known canonical agent names.
var Agents = map[string]bool{
	"claude": true, "codex": true, "opencode": true, "pi": true,
	"qwen": true, "copilot": true, "kimi": true, "kimi-cli": true,
	"mcode": true, "muse": true, "vibe": true, "gemini": true,
	"cline": true, "cursor-agent": true, "grok": true, "crush": true,
	"omp": true, "kilo": true, "antigravity": true, "deepseek": true,
	"dsh-tui": true, "fx": true, "fx-router": true,
}

// Shells is the set of shell executable basenames.
var Shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true,
	"nu": true, "ksh": true, "tcsh": true, "csh": true,
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// Basename returns the final path component, trimming trailing slashes.
func Basename(value string) string {
	return filepath.Base(strings.TrimRight(value, "/"))
}

// CurrentDirectory returns the working directory with PWD/HOME fallbacks.
func CurrentDirectory() string {
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	if pwd := os.Getenv("PWD"); pwd != "" {
		return pwd
	}
	return homeDir()
}

// DisplayApp returns the canonical app label used in OSC 7501 records.
func DisplayApp(agentName string) string {
	name := strings.ToLower(strings.TrimSpace(Basename(agentName)))
	if p, ok := agent.DefaultRegistry().ResolveRuntime(name); ok {
		return p.Spec().EffectiveCLIDisplayApp()
	}
	if normalized := Normalize(name); normalized != "" {
		name = normalized
	}
	if app, ok := AppNames[name]; ok {
		return app
	}
	return name
}

// OSCStates maps normalized hook states to OSC pane statuses.
var OSCStates = map[string]string{
	"working": "working",
	"blocked": "blocked",
	"done":    "done",
	"failed":  "error",
	"idle":    "idle",
}

func stdoutFile() *os.File {
	if Stdout != nil {
		return Stdout
	}
	return os.Stdout
}

func stderrFile() *os.File {
	if Stderr != nil {
		return Stderr
	}
	return os.Stderr
}

func errf(format string, args ...any) {
	fmt.Fprintf(stderrFile(), format, args...)
}

func out(s string) {
	fmt.Fprintln(stdoutFile(), s)
}

func discoverTitle(agentName, cwd, sessionID string) string {
	if DiscoverTitleFunc == nil {
		return ""
	}
	return strings.TrimSpace(DiscoverTitleFunc(agentName, cwd, sessionID))
}

// environMap snapshots os.Environ into a map for the normalize seam.
func environMap() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	return env
}

func helperPath() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		if abs, err := filepath.Abs(exe); err == nil {
			return abs
		}
		return exe
	}
	return "tmux-spaces"
}

// --- shlex (POSIX) ----------------------------------------------------------

// shlexSplit splits a command line following POSIX shell quoting rules.
func shlexSplit(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	var inSingle, inDouble bool
	hasToken := false
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
			i++
		case inDouble:
			if c == '"' {
				inDouble = false
				i++
			} else if c == '\\' && i+1 < len(s) {
				n := s[i+1]
				if n == '"' || n == '\\' || n == '$' || n == '`' || n == '\n' {
					cur.WriteByte(n)
					i += 2
				} else {
					cur.WriteByte(c)
					i++
				}
			} else {
				cur.WriteByte(c)
				i++
			}
		case c == '\'':
			inSingle = true
			hasToken = true
			i++
		case c == '"':
			inDouble = true
			hasToken = true
			i++
		case c == '\\':
			if i+1 >= len(s) {
				return nil, errors.New("shlex: no escaped character")
			}
			n := s[i+1]
			if n == '\n' {
				i += 2
			} else {
				cur.WriteByte(n)
				i += 2
				hasToken = true
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
			i++
		default:
			cur.WriteByte(c)
			hasToken = true
			i++
		}
	}
	if inSingle || inDouble {
		return nil, errors.New("shlex: no closing quotation")
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args, nil
}

// --- Python-compatible JSON -------------------------------------------------

// marshalValue renders v with sorted map keys. Compact selects separators
// (",", ":"); spaced selects (", ", ": "). When ascii is true, non-ASCII is
// escaped as \uXXXX and Go's \u0008 \u000c become \b \f.
func marshalValue(v any, compact, ascii bool) string {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "null"
	}
	s := strings.TrimSuffix(sb.String(), "\n")
	s = strings.ReplaceAll(s, `\u0008`, `\b`)
	s = strings.ReplaceAll(s, `\u000c`, `\f`)
	if !compact {
		s = spacify(s)
	}
	if ascii {
		s = escapeNonASCII(s)
	}
	return s
}

// marshalPython renders v with sorted map keys and compact separators.
func marshalPython(v any, ascii bool) string {
	return marshalValue(v, true, ascii)
}

// jsonField is one ordered object member.
type jsonField struct {
	Key string
	Val any
}

// marshalObject renders ordered members as a JSON object, preserving insertion order.
func marshalObject(fields []jsonField, compact, ascii bool) string {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			if compact {
				sb.WriteByte(',')
			} else {
				sb.WriteString(", ")
			}
		}
		sb.WriteString(marshalValue(f.Key, true, false))
		if compact {
			sb.WriteByte(':')
		} else {
			sb.WriteString(": ")
		}
		sb.WriteString(marshalValue(f.Val, compact, ascii))
	}
	sb.WriteByte('}')
	return sb.String()
}

func spacify(s string) string {
	var sb strings.Builder
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			sb.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			sb.WriteByte(c)
		case ':', ',':
			sb.WriteByte(c)
			sb.WriteByte(' ')
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func escapeNonASCII(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r < 0x20 || r == '"' || r == '\\' {
			sb.WriteRune(r)
			continue
		}
		if r < 0x80 {
			sb.WriteRune(r)
			continue
		}
		if r <= 0xFFFF {
			fmt.Fprintf(&sb, `\u%04x`, r)
			continue
		}
		r -= 0x10000
		fmt.Fprintf(&sb, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
	}
	return sb.String()
}

func b64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func itoa(n int) string { return strconv.Itoa(n) }

func splitSScan(s string, v *int) (int, error) { return fmt.Sscanf(s, "%d", v) }

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	case nil:
		return false
	default:
		return true
	}
}
