package client

import (
	"os"
	"strconv"
	"strings"
)

// Hook is the `hook AGENT [Event]` entry: normalize the hook payload,
// emit the OSC/tmux record, publish or spool. With an empty agent it exits 0.
func Hook(agent, event string) int {
	if strings.TrimSpace(agent) == "" {
		return 0
	}
	return HookClient(agent, event)
}

// SessionHook is the `session-hook AGENT [Event]` entry (backward-compatible
// alias for the hook client).
func SessionHook(agent, event string) int {
	return HookClient(agent, event)
}

// HookClient is the agent-facing hook entry point: it reads the provider JSON
// on stdin, normalizes via the seam, emits the OSC/tmux record, publishes or
// spools, and records the resume target. Outside tmux/Zellij, or for
// non-interactive ancestors, it exits 0 silently.
func HookClient(agentName, event string) int {
	return hookClientWithPayload(agentName, event, nil, true)
}

func hookClientWithPayload(agentName, event string, payload map[string]any, transport bool) int {
	agent := Normalize(agentName)
	if agent == "" {
		agent = agentName
	}
	if payload == nil {
		payload = ReadHookPayload(0.5)
	}
	if AgentIsNoninteractive(agent) {
		return 0
	}
	session, pane, paneRef, ok := hookTargetForAgent(agent, payload)
	if !ok {
		// Outside tmux/Zellij there is no pane to attribute the event to.
		return 0
	}
	if HookDisabled == nil || HookDisabled() {
		EmitAgentEvent(agent, event)
		return 0
	}
	if NormalizeEvent == nil {
		EmitAgentEvent(agent, event)
		return 0
	}
	envMap := environMap()
	if paneRef != "" && paneRef != os.Getenv("TMUX_PANE") {
		delete(envMap, "HARNESS_PROFILE")
	}
	normalized := NormalizeEvent(agent, event, payload, envMap)
	if normalized == nil {
		return 0
	}
	if ResolvePaneEvent != nil {
		normalized = ResolvePaneEvent(session, pane, normalized)
	}
	if ev, _ := normalized["event"].(string); (ev == "session_start" || ev == "prompt" || ev == "pre_invocation" || ev == "stop" || ev == "stop_failure" || ev == "session_end") &&
		!truthy(normalized["child"]) {
		sid := ExtractSessionID(payload)
		if title, _ := normalized["title"].(string); strings.TrimSpace(title) == "" {
			cwd, _ := normalized["cwd"].(string)
			if cwd == "" {
				cwd = CurrentDirectory()
			}
			if discovered := discoverTitle(agent, cwd, sid); discovered != "" {
				normalized["title"] = discovered
			}
		}
		EmitHookStatusToPane(paneRef, normalized)
		RecordResumeTargetToPane(paneRef, agent, payload)
		if ev == "stop" {
			if st, _ := normalized["state"].(string); st == "done" {
				UnsetTmuxPaneOption(paneRef, "@spaces_prior_outcome")
			}
		}
	} else {
		EmitHookStatusToPane(paneRef, normalized)
	}
	if transport {
		envelope := map[string]any{
			"session": session, "pane": pane,
			"agent": agent, "event": event,
			"payload": payload, "normalized": normalized,
		}
		published := false
		if Publish != nil {
			published = Publish(envelope)
		}
		if !published {
			if AppendSpool != nil {
				entry := map[string]any{"session": session, "pane": pane}
				for k, v := range normalized {
					entry[k] = v
				}
				_ = AppendSpool(entry)
			}
			if EnsureServer != nil {
				_ = EnsureServer(helperPath())
			}
		}
	}
	return 0
}

// Hooks is the `hooks [status|start|stop|restart|serve]` entry.
func Hooks(args []string) int {
	if HooksStatus == nil {
		errf("hooks: hook core unavailable\n")
		return 2
	}
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "status":
		return HooksStatus()
	case "stop":
		if HooksStop == nil {
			errf("hooks: hook core unavailable\n")
			return 2
		}
		return HooksStop()
	case "start", "restart":
		if HooksStart == nil {
			errf("hooks: hook core unavailable\n")
			return 2
		}
		return HooksStart(helperPath())
	case "serve":
		if Serve == nil {
			errf("hooks: hook core unavailable\n")
			return 2
		}
		return Serve(true, nil)
	default:
		errf("usage: hooks [status|start|stop|restart|serve]\n")
		return 2
	}
}

// HookServer is the `hook-server [--daemon] [--tcp PORT]` entry.
func HookServer(args []string) int {
	if Daemonize == nil && Serve == nil {
		return 2
	}
	daemon := false
	var tcpPort *int
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--daemon":
			daemon = true
		case "--foreground":
			// Default; accepted for the CLI contract.
		case "--tcp":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					tcpPort = &n
				}
				i++
			}
		default:
			if n, err := strconv.Atoi(args[i]); err == nil && tcpPort == nil {
				tcpPort = &n
			}
		}
	}
	if daemon {
		if Daemonize == nil {
			return 2
		}
		return Daemonize()
	}
	if Serve == nil {
		return 2
	}
	return Serve(true, tcpPort)
}
