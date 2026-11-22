package model

import (
	"strings"
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

// knownAgents is the union of all bundled screen-manifest agents, their
// aliases, and OSC-authority harnesses.
var knownAgents = map[string]bool{
	"agy": true, "antigravity": true, "antigravity-cli": true,
	"amp": true, "amp-local": true,
	"claude": true, "claude-code": true,
	"cline":   true,
	"codex":   true,
	"copilot": true, "github-copilot": true, "ghcs": true,
	"crush":  true,
	"cursor": true, "cursor-agent": true,
	"deepseek": true, "dsh": true, "dsh-tui": true,
	"devin": true, "devin-cli": true,
	"droid": true,
	"fx":    true, "fx-router": true,
	"gemini": true,
	"grok":   true, "grok-build": true,
	"hermes": true, "hermes-agent": true,
	"kilo": true, "kilo-code": true,
	"kimi": true, "kimi-cli": true, "kimi-code": true,
	"kiro": true, "kiro-cli": true,
	"letta": true, "letta-code": true,
	"maki":  true,
	"mcode": true,
	"muse":  true, "muse-code": true, "muse-cli": true,
	"omp":      true,
	"opencode": true, "open-code": true,
	"pi":       true,
	"qodercli": true, "qoderclicn": true, "qoder": true, "qodercn": true,
	"qwen": true, "qwen-code": true,
	"vibe": true, "vibe-acp": true,
}

// NormalizeAgentCommand normalizes a binary/app name to a known agent key
// when possible (e.g. "/usr/bin/muse-bin-1.4.24-R9131.1" -> "muse",
// "herdr:opencode" -> "opencode"). Unknown names are returned normalized.
func NormalizeAgentCommand(app string) string {
	n := strings.ToLower(strings.TrimSpace(app))
	if n == "" {
		return ""
	}
	n = strings.TrimPrefix(n, "herdr:")
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".ps1", ".js", ".mjs", ".cjs"} {
		if strings.HasSuffix(n, suffix) {
			n = strings.TrimSuffix(n, suffix)
			break
		}
	}
	if knownAgents[n] {
		return n
	}
	// Versioned binary wrappers such as "muse-bin-1.4.24-R9131.1".
	if prefix, _, ok := strings.Cut(n, "-bin-"); ok && knownAgents[prefix] {
		return prefix
	}
	// Versioned binaries such as "claude-2.1.76" or "codex-0.42.0".
	if idx := strings.IndexByte(n, '-'); idx > 0 && idx+1 < len(n) && n[idx+1] >= '0' && n[idx+1] <= '9' {
		if prefix := n[:idx]; knownAgents[prefix] {
			return prefix
		}
	}
	return n
}

// IsKnownAgent reports whether app names a known coding-agent harness.
func IsKnownAgent(app string) bool {
	n := NormalizeAgentCommand(app)
	return n != "" && knownAgents[n]
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
