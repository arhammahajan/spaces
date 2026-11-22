package cli

import (
	"encoding/json"
	"strings"
)

// Catppuccin Mocha accents for the status-line badge, matching theme.go.
const (
	mochaBlue   = "#89B4FA" // needs you
	mochaRed    = "#F38BA8" // failed
	mochaYellow = "#F9E2AF" // working
	mochaGreen  = "#A6E3A1" // done
)

type statusCounts struct {
	Needs   int `json:"needs"`
	Failed  int `json:"failed"`
	Working int `json:"working"`
	Done    int `json:"done"`
	Idle    int `json:"idle"`
}

func countSnapshotPanes(env Env) (statusCounts, error) {
	var c statusCounts
	snap, err := env.loadSpaces()
	if err != nil {
		return c, err
	}
	for _, p := range snap.Panes {
		switch bucketOf(p.SpacesState) {
		case BucketNeeds:
			c.Needs++
		case BucketFailed:
			c.Failed++
		case BucketWorking:
			c.Working++
		case BucketDone:
			c.Done++
		default:
			c.Idle++
		}
	}
	return c, nil
}

// cmdStatusBar prints a compact status-line badge segment. It prints
// nothing at all when every pane is idle, so the status line stays clean.
func cmdStatusBar(args []string, env Env) int {
	f, err := parseFlags(args, nil, []string{"plain", "json"}, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces status-bar [--plain] [--json]\n")
			return ExitOK
		}
		env.errf("tmux-spaces status-bar: %s\n", err)
		return ExitUsage
	}
	c, err := countSnapshotPanes(env)
	if err != nil {
		env.errf("tmux-spaces status-bar: %s\n", err)
		return ExitError
	}
	if env.MaybeAutoSave != nil {
		env.MaybeAutoSave()
	}
	if f.set["json"] {
		raw, _ := json.Marshal(c)
		env.outf("%s\n", raw)
		return ExitOK
	}
	if c.Needs+c.Failed+c.Working+c.Done == 0 {
		return ExitOK
	}
	type seg struct {
		n     int
		icon  string
		color string
	}
	segs := []seg{
		{c.Needs, "●", mochaBlue},
		{c.Failed, "✗", mochaRed},
		{c.Working, "◐", mochaYellow},
		{c.Done, "✓", mochaGreen},
	}
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		if s.n == 0 {
			continue
		}
		text := s.icon + itoa(s.n)
		if f.set["plain"] {
			parts = append(parts, text)
			continue
		}
		parts = append(parts, "#[fg="+s.color+"]"+text+"#[default]")
	}
	env.outf("%s\n", strings.Join(parts, " "))
	return ExitOK
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
