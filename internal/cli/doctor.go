package cli

import (
	"encoding/json"
)

// doctorCheck is one row of the doctor report.
type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// cmdDoctor checks the tmux binary, server, session context, helper
// binaries, and hook listener health.
func cmdDoctor(args []string, env Env) int {
	f, err := parseFlags(args, nil, []string{"json"}, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces doctor [--json]\n")
			return ExitOK
		}
		env.errf("tmux-spaces doctor: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces doctor [--json]\n")
		return ExitUsage
	}
	checks := []doctorCheck{}

	tmuxBin := env.Getenv("TMUX_BIN")
	if tmuxBin == "" {
		tmuxBin = "tmux"
	}
	binPath, binErr := env.LookPath(tmuxBin)
	if binErr != nil {
		checks = append(checks, doctorCheck{"tmux", false, "not found on PATH"})
	} else {
		checks = append(checks, doctorCheck{"tmux", true, binPath})
	}

	if env.Client == nil {
		checks = append(checks, doctorCheck{"server", false, "no tmux client"})
	} else if env.Client.ServerRunning() {
		checks = append(checks, doctorCheck{"server", true, "running"})
	} else {
		checks = append(checks, doctorCheck{"server", false, "no tmux server (start tmux first)"})
	}

	if tmuxEnv := env.Getenv("TMUX"); tmuxEnv != "" {
		checks = append(checks, doctorCheck{"session", true, "inside tmux"})
	} else {
		checks = append(checks, doctorCheck{"session", true, "outside tmux"})
	}

	if env.HookHealth == nil {
		checks = append(checks, doctorCheck{"hooks", true, "unknown (hook check not wired)"})
	} else if detail, herr := env.HookHealth(); herr != nil {
		checks = append(checks, doctorCheck{"hooks", false, herr.Error()})
	} else if detail == "" {
		checks = append(checks, doctorCheck{"hooks", true, "listener healthy"})
	} else {
		checks = append(checks, doctorCheck{"hooks", true, detail})
	}

	// Companion plugins are informational only: fallback screen scraping
	// always works, so this row never gates the exit code.
	if env.PluginsHealth == nil {
		if detail, _ := pluginsHealthSummary(pluginHomeDir(env, ""), pluginLiveOf(env)); detail != "" {
			checks = append(checks, doctorCheck{"plugins", true, detail})
		} else {
			checks = append(checks, doctorCheck{"plugins", true, "no companion plugins registered (fallback mode)"})
		}
	} else if detail, herr := env.PluginsHealth(); herr != nil {
		checks = append(checks, doctorCheck{"plugins", true, "fallback mode (" + herr.Error() + ")"})
	} else if detail == "" {
		checks = append(checks, doctorCheck{"plugins", true, "fallback mode"})
	} else {
		checks = append(checks, doctorCheck{"plugins", true, detail})
	}

	ok := true
	for _, c := range checks {
		// The session-context row is informational; tmux + server gate the exit code.
		if (c.Name == "tmux" || c.Name == "server") && !c.OK {
			ok = false
		}
	}
	if f.set["json"] {
		raw, _ := json.Marshal(map[string]any{"ok": ok, "checks": checks})
		env.outf("%s\n", raw)
	} else {
		for _, c := range checks {
			mark := "ok"
			if !c.OK {
				mark = "FAIL"
			}
			env.outf("%-8s %-4s %s\n", c.Name+":", mark, c.Detail)
		}
	}
	if ok {
		return ExitOK
	}
	return ExitError
}
