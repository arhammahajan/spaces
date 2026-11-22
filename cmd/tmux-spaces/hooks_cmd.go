package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	hookclient "github.com/arhammahajan/spaces/internal/hooks/client"
	hookcore "github.com/arhammahajan/spaces/internal/hooks/core"
	"github.com/arhammahajan/spaces/internal/model"
)

var bindHooksOnce sync.Once

func bindHookSeams() {
	bindHooksOnce.Do(func() {
		resolveTitle := func(agentName, cwd, sessionID string) string {
			if strings.TrimSpace(agentName) == "" {
				return ""
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			titles, err := model.FetchConversationTitles(ctx, []model.TitleQuery{{
				Key:       "q",
				Agent:     agentName,
				CWD:       cwd,
				SessionID: sessionID,
			}})
			if err != nil {
				return ""
			}
			return strings.TrimSpace(titles["q"])
		}

		hookclient.HookDisabled = hookcore.Disabled
		hookclient.NormalizeEvent = hookcore.NormalizeEvent
		hookclient.ResolvePaneEvent = func(session, pane string, normalized map[string]any) map[string]any {
			return hookcore.ResolvePaneEvent(session, pane, normalized)
		}
		hookclient.Publish = hookcore.Publish
		hookclient.AppendSpool = hookcore.AppendSpool
		hookclient.EnsureServer = func(helperPath string) bool {
			return hookcore.EnsureServer(helperPath) == nil
		}
		hookclient.SpaceHooksSummary = hookcore.SpaceHooksSummary
		hookclient.SpaceHooksDetail = hookcore.SpaceHooksDetail
		hookclient.RecordOutcome = hookcore.RecordOutcome
		hookclient.RemoveOutcome = hookcore.RemoveOutcome
		hookclient.PaneState = hookcore.PaneState
		hookclient.HooksStatus = hookcore.HooksStatus
		hookclient.HooksStop = hookcore.HooksStop
		hookclient.HooksStart = hookcore.HooksStart
		hookclient.Daemonize = hookcore.Daemonize
		hookclient.Serve = func(foreground bool, tcpPort *int) int {
			port := 0
			if tcpPort != nil {
				port = *tcpPort
			}
			return hookcore.Serve(foreground, port)
		}
		hookclient.DiscoverTitleFunc = resolveTitle
		hookcore.SpaceHookTitleResolver = resolveTitle
		hookcore.SpaceHookProfileResolver = func(agentName, sessionID string) string {
			return hookclient.DiscoverProfile(agentName, "", sessionID)
		}
		model.OutcomeEnricher = func(o *model.OutcomeEntry) {
			if o == nil {
				return
			}
			m := map[string]any{
				"session":    o.Session,
				"pane":       o.Pane,
				"agent":      o.Agent,
				"app":        o.App,
				"profile":    o.Profile,
				"outcome":    o.Outcome,
				"text":       o.Text,
				"title":      o.Title,
				"model":      o.Model,
				"session_id": o.SessionID,
				"cwd":        o.CWD,
				"ts_ms":      o.TsMs,
			}
			hookclient.EnrichOutcomeMap(m)
			if s, ok := m["session"].(string); ok {
				o.Session = s
			}
			if s, ok := m["pane"].(string); ok {
				o.Pane = s
			}
			if s, ok := m["agent"].(string); ok {
				o.Agent = s
			}
			if s, ok := m["app"].(string); ok {
				o.App = s
			}
			if s, ok := m["profile"].(string); ok {
				o.Profile = s
			}
			if s, ok := m["outcome"].(string); ok {
				o.Outcome = s
			}
			if s, ok := m["text"].(string); ok {
				o.Text = s
			}
			if s, ok := m["title"].(string); ok {
				o.Title = s
			}
			if s, ok := m["model"].(string); ok {
				o.Model = s
			}
			if s, ok := m["session_id"].(string); ok {
				o.SessionID = s
			}
			if s, ok := m["cwd"].(string); ok {
				o.CWD = s
			}
		}
	})
}

// maybeRunHookCommand handles hook receiver/daemon/store commands directly
// (invoked either as `tmux-spaces <cmd>` or via the `spaces` symlink).
func maybeRunHookCommand(argv0 string, args []string) (int, bool) {
	prog := filepath.Base(argv0)
	if len(args) == 0 {
		if prog == "spaces" {
			fmt.Fprintf(os.Stderr, "usage: spaces <hook|session-hook|hooks|hook-server|__space-hooks> [args...]\n")
			return 2, true
		}
		return 0, false
	}

	command := args[0]
	rest := args[1:]
	second := func() string {
		if len(rest) > 1 {
			return rest[1]
		}
		return ""
	}

	switch command {
	case "hook":
		bindHookSeams()
		if len(rest) == 0 {
			fmt.Fprintf(os.Stderr, "%s: hook needs an agent\n", prog)
			return 1, true
		}
		return hookclient.Hook(rest[0], second()), true
	case "__hook":
		bindHookSeams()
		return hookclient.Hook("", ""), true
	case "session-hook":
		bindHookSeams()
		if len(rest) == 0 {
			fmt.Fprintf(os.Stderr, "%s: session-hook needs an agent\n", prog)
			return 1, true
		}
		return hookclient.SessionHook(rest[0], second()), true
	case "hooks":
		bindHookSeams()
		return hookclient.Hooks(rest), true
	case "hook-server":
		bindHookSeams()
		return hookclient.HookServer(rest), true
	case "__space-hooks":
		bindHookSeams()
		return hookclient.SpaceHooks(rest), true
	}
	return 0, false
}
