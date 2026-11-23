package cli

import (
	"encoding/json"

	"github.com/arhammahajan/spaces/internal/tmux"
)

// cmdPanes lists panes with status, app, window, pane id, title, and CWD.
func cmdPanes(args []string, env Env) int {
	f, err := parseFlags(args, []string{"session"}, []string{"json"}, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces panes [--session S] [--json]\n")
			return ExitOK
		}
		env.errf("tmux-spaces panes: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces panes [--session S] [--json]\n")
		return ExitUsage
	}
	type row struct {
		Session string `json:"session"`
		Window  string `json:"window"`
		Pane    string `json:"pane"`
		Status  string `json:"status"`
		App     string `json:"app"`
		Title   string `json:"title"`
		CWD     string `json:"cwd"`
		Active  bool   `json:"active"`
		Dead    bool   `json:"dead"`
		PID     int    `json:"pid"`
		Source  string `json:"source,omitempty"`
	}
	rows := []row{}
	if session := f.str["session"]; session != "" {
		client, cerr := env.requireClient()
		if cerr != nil {
			env.errf("tmux-spaces panes: %s\n", cerr)
			return ExitError
		}
		panes, perr := client.ListPanes(session)
		if perr != nil {
			env.errf("tmux-spaces panes: %s\n", perr)
			return ExitError
		}
		for _, p := range panes {
			rows = append(rows, row{
				Session: p.SessionName, Window: windowLabel(p.WindowName, p.WindowID),
				Pane: p.ID, Status: bucketOf(p.SpacesState), App: paneApp(&p),
				Title: p.Title, CWD: p.CurrentPath,
				Active: p.Active, Dead: p.Dead, PID: p.PID,
				Source: p.SpacesSource,
			})
		}
	} else {
		snap, serr := env.loadSpaces()
		if serr != nil {
			env.errf("tmux-spaces panes: %s\n", serr)
			return ExitError
		}
		for _, p := range snap.Panes {
			rows = append(rows, row{
				Session: p.SessionName, Window: windowLabel(p.WindowName, p.WindowID),
				Pane: p.ID, Status: bucketOf(p.SpacesState), App: paneApp(&p),
				Title: p.Title, CWD: p.CurrentPath,
				Active: p.Active, Dead: p.Dead, PID: p.PID,
				Source: p.SpacesSource,
			})
		}
	}
	if f.set["json"] {
		if rows == nil {
			rows = []row{}
		}
		raw, _ := json.Marshal(rows)
		env.outf("%s\n", raw)
		return ExitOK
	}
	env.outf("  %-18s %-12s %-6s %-8s %-14s %-20s  %s\n",
		"SESSION", "WINDOW", "PANE", "STATUS", "APP", "TITLE", "CWD")
	for _, r := range rows {
		status := r.Status
		if r.Dead {
			status += " (dead)"
		}
		env.outf("  %-18s %-12s %-6s %-8s %-14s %-20s  %s\n",
			trunc(r.Session, 18), trunc(r.Window, 12), r.Pane,
			status, trunc(r.App, 14), trunc(r.Title, 20), r.CWD)
	}
	return ExitOK
}

func windowLabel(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

func paneApp(p *tmux.Pane) string {
	if p.SpacesApp != "" {
		return p.SpacesApp
	}
	if p.CurrentCommand != "" {
		return p.CurrentCommand
	}
	return "-"
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
