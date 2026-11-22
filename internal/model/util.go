package model

import (
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	_ "github.com/arhammahajan/spaces/internal/agent/plugins"
)

// IsShell reports whether name is a shell executable.
func IsShell(name string) bool {
	n := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "-")
	switch n {
	case "sh", "bash", "zsh", "fish", "nu", "nushell", "dash", "ksh", "tcsh", "csh":
		return true
	default:
		return false
	}
}

// isShellApp reports whether app is the normalized "shell" label or a shell
// executable name.
func isShellApp(app string) bool {
	n := strings.ToLower(strings.TrimSpace(app))
	return n == "shell" || IsShell(n)
}

// NormalizeAgentCommand normalizes a binary/app name to a known agent key
// when possible (e.g. "/usr/bin/muse-bin-1.4.24-R9131.1" -> "muse",
// "herdr:opencode" -> "opencode"). Unknown names are returned normalized.
func NormalizeAgentCommand(app string) string {
	return agent.NormalizeCommand(app)
}

// IsKnownAgent reports whether app names a known coding-agent harness.
func IsKnownAgent(app string) bool {
	return agent.DefaultRegistry().IsKnown(app)
}

// CommandName returns the executable basename of a command line
// ("opencode --auto" -> "opencode", "/usr/bin/pi" -> "pi").
// It returns "" when the command is blank.
func CommandName(command string) string {
	first := strings.TrimSpace(command)
	if i := strings.IndexAny(first, " \t"); i >= 0 {
		first = first[:i]
	}
	base := strings.TrimRight(first, "/")
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base
}

// FormatAge formats an age in milliseconds compactly:
// "now" (<5s), "12s", "5m", "3h", "2d".
func FormatAge(ageMs int64) string {
	if ageMs < 0 {
		ageMs = 0
	}
	secs := ageMs / 1000
	switch {
	case secs < 5:
		return "now"
	case secs < 60:
		return strings.Join([]string{itoa(secs), "s"}, "")
	case secs < 60*60:
		return strings.Join([]string{itoa(secs / 60), "m"}, "")
	case secs < 24*60*60:
		return strings.Join([]string{itoa(secs / (60 * 60)), "h"}, "")
	default:
		return strings.Join([]string{itoa(secs / (24 * 60 * 60)), "d"}, "")
	}
}

// itoa formats a small non-negative integer without importing strconv
// into every caller; util stays dependency-light.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [32]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ShortenHome renders path with a leading `~` when it sits under home.
// ("~/…" form only; no truncation.)
func ShortenHome(path, home string) string {
	home = strings.TrimRight(home, "/")
	if home == "" || home == "/" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

// SanitizeSpaceName trims name and returns "" when it is not usable as a
// tmux session name: empty, containing '/', or containing control
// characters.
func SanitizeSpaceName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.Contains(name, "/") {
		return ""
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return name
}
