package osc

import (
	"os"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

// CanonicalApp maps an agent name to the app label carried in OSC 7501
// records via agent.DefaultRegistry().CLIDisplayApp, falling back to the
// lowercased, trimmed input when unknown.
func CanonicalApp(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return agent.DefaultRegistry().CLIDisplayApp(strings.ToLower(trimmed))
}

// Enabled reports whether attention state should be reported from this pane:
// inside a Zellij session (ZELLIJ_SESSION_NAME non-empty) or inside a tmux
// pane (TMUX and TMUX_PANE non-empty), with a writable record target (see
// TtyWriter), and not opted out through HARNESS_ATTN — "0"/"false" disable,
// a present-but-empty value means enabled. T3 invocations (HARNESS_AGENT set)
// still report: their panes are real.
func Enabled() bool {
	inZellij := os.Getenv("ZELLIJ_SESSION_NAME") != ""
	inTmux := os.Getenv("TMUX") != "" && os.Getenv("TMUX_PANE") != ""
	if !inZellij && !inTmux {
		return false
	}
	switch os.Getenv("HARNESS_ATTN") {
	case "0", "false":
		return false
	}
	return TtyWritable()
}
