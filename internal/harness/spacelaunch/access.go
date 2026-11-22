package spacelaunch

// Launch-time access-level selection. Before launching a harness that has
// known sandbox/approval flags, offer the supported levels on the terminal.
// T3 Code invocations (detected via HARNESS_AGENT, the variable its provider
// instances set) are never offered a choice: T3 passes its own flags.
//
// Moved here from cmd/harness so the profile shim (which resolves and
// launches outside the main package) shares the exact offering behavior.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// ErrAccessCancelled reports a deliberate launch cancellation at the prompt.
var ErrAccessCancelled = errors.New("cancelled")

// Injectable seams for tests.
var (
	accessIn        io.Reader = os.Stdin
	accessOut       io.Writer = os.Stderr
	stdinIsTerminal           = func() bool {
		info, err := os.Stdin.Stat()
		if err != nil {
			return false
		}
		return info.Mode()&os.ModeCharDevice != 0
	}
)

// ApplyAccessLevel offers the agent's sandbox/access levels before launch
// and layers the chosen level onto the launch spec. Skip conditions, in
// order:
//
//  1. HARNESS_AGENT is set: T3 Code invokes the binary this way and passes
//     its own flags; nothing is offered or injected.
//  2. The agent has no known levels.
//  3. HARNESS_ACCESS names a level: applied without prompting.
//  4. stdin is not a terminal: there is no one to ask.
//
// ErrAccessCancelled means the user answered "q" at the prompt; callers
// should report a clean cancel. Any other error is a usage error.
func ApplyAccessLevel(spec *harnessenv.LaunchSpec) error {
	if err := PreselectAccessLevel(spec); err != nil {
		return err
	}
	return PromptAccessLevelForLaunch(spec)
}

// PreselectAccessLevel applies a deterministic HARNESS_ACCESS choice and
// never prompts. Launches, `describe`, and other resolutions share it.
func PreselectAccessLevel(spec *harnessenv.LaunchSpec) error {
	if os.Getenv("HARNESS_AGENT") != "" {
		// T3 passes its own flags; leave the invocation untouched.
		return nil
	}
	levels := agents.AccessLevels(spec.Agent)
	if len(levels) == 0 {
		return nil
	}
	if id := strings.TrimSpace(os.Getenv("HARNESS_ACCESS")); id != "" {
		level, ok := agents.FindAccessLevel(spec.Agent, id)
		if !ok {
			return fmt.Errorf("unknown access level %q for %s (available: %s)",
				id, spec.Agent, levelIDs(levels))
		}
		layerAccessLevel(spec, level)
	}
	return nil
}

// promptAccessLevelForLaunch offers the interactive picker. It is a no-op
// unless the agent has levels and stdin is a terminal; HARNESS_AGENT (T3)
// invocations never reach it, and an explicit HARNESS_ACCESS preselect has
// already decided the level.
func PromptAccessLevelForLaunch(spec *harnessenv.LaunchSpec) error {
	if os.Getenv("HARNESS_AGENT") != "" {
		return nil
	}
	if strings.TrimSpace(os.Getenv("HARNESS_ACCESS")) != "" {
		return nil
	}
	levels := agents.AccessLevels(spec.Agent)
	if len(levels) == 0 || !stdinIsTerminal() {
		return nil
	}
	level, ok := promptAccessLevel(spec.Agent, levels)
	if !ok {
		return ErrAccessCancelled
	}
	layerAccessLevel(spec, level)
	return nil
}

// layerAccessLevel prepends the level's flags ahead of every other
// argument so they land before any subcommand the caller passes.
func layerAccessLevel(spec *harnessenv.LaunchSpec, level agents.AccessLevel) {
	if len(level.Args) > 0 {
		spec.PrependArgs = append(append([]string{}, level.Args...), spec.PrependArgs...)
	}
	if len(level.Env) > 0 {
		if spec.Env == nil {
			spec.Env = map[string]string{}
		}
		for key, value := range level.Env {
			spec.Env[key] = value
		}
	}
}

// promptAccessLevel renders the numbered option list and reads the choice
// from accessIn. Enter selects the default level; "q" cancels; unknown
// input re-prompts.
func promptAccessLevel(agent string, levels []agents.AccessLevel) (agents.AccessLevel, bool) {
	defaultIndex := 0
	for i, level := range levels {
		if level.ID == "default" {
			defaultIndex = i
		}
	}
	hint := fmt.Sprintf("1-%d, Enter=%s, q=quit", len(levels), levels[defaultIndex].Label)
	fmt.Fprintf(accessOut, "\nAccess level for %s:\n", agent)
	for i, level := range levels {
		marker := "  "
		if i == defaultIndex {
			marker = "> "
		}
		fmt.Fprintf(accessOut, "%s%d) %-14s %s\n", marker, i+1, level.Label, level.Desc)
	}
	fmt.Fprintf(accessOut, "Choice [%s]: ", hint)
	reader := bufio.NewReader(accessIn)
	for {
		line, readErr := reader.ReadString('\n')
		input := strings.TrimSpace(line)
		if input == "" {
			if readErr != nil && line == "" {
				return agents.AccessLevel{}, false // EOF: cancel instead of guessing
			}
			return levels[defaultIndex], true
		}
		switch strings.ToLower(input) {
		case "q", "quit", "cancel":
			return agents.AccessLevel{}, false
		}
		if n, err := strconv.Atoi(input); err == nil && n >= 1 && n <= len(levels) {
			return levels[n-1], true
		}
		for _, level := range levels {
			if strings.EqualFold(level.ID, input) || strings.EqualFold(level.Label, input) {
				return level, true
			}
		}
		fmt.Fprintf(accessOut, "Invalid choice %q; try again [%s]: ", input, hint)
	}
}

func levelIDs(levels []agents.AccessLevel) string {
	ids := make([]string, len(levels))
	for i, level := range levels {
		ids[i] = level.ID
	}
	return strings.Join(ids, ", ")
}
