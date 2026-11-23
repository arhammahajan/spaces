package agent

import (
	"path/filepath"
	"strings"
)

const ResumeAccessCommand = "__resume-access"

// HarnessLaunchArgs recognizes Spaces launcher commands stored by current or
// older versions. It leaves native agent commands alone.
func HarnessLaunchArgs(argv []string) (args []string, level string, ok bool) {
	if len(argv) < 2 {
		return nil, "", false
	}
	if argv[1] == ResumeAccessCommand && len(argv) >= 4 {
		return argv[3:], argv[2], true
	}
	switch filepath.Base(argv[0]) {
	case "harness", "spaces", "tmux-spaces":
		switch argv[1] {
		case "profile", "launch":
			return argv[1:], "", true
		}
		if DefaultRegistry().CanonicalID(argv[1]) != "" {
			return argv[1:], "", true
		}
	}
	return nil, "", false
}

// HarnessResumeCommand uses an empty level to look up an older record by its
// conversation ID, then falls back to the agent's configured default.
func HarnessResumeCommand(self, level string, args ...string) []string {
	return append([]string{self, ResumeAccessCommand, level}, args...)
}

// HarnessProfileCommand provides the same launch shape for recording and replay.
func HarnessProfileCommand(name, profile string, args ...string) []string {
	if canonical := DefaultRegistry().CanonicalID(name); canonical != "" {
		name = canonical
	}
	if strings.TrimSpace(profile) == "" {
		profile = "default"
	}
	return append([]string{name, "profile", profile}, args...)
}
