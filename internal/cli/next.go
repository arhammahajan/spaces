package cli

import (
	"sort"

	"github.com/arhammahajan/spaces/internal/tmux"
)

// nextTarget picks the highest-priority attention pane (NeedsYou > Failed >
// Done) across all sessions. Ties break deterministically by session,
// window index, and pane id.
func nextTarget(snap tmux.ServerSnapshot) (tmux.Pane, string, bool) {
	best := -1
	var bestPane tmux.Pane
	order := append([]tmux.Pane{}, snap.Panes...)
	sort.Slice(order, func(i, j int) bool {
		bi, bj := bucketOf(order[i].SpacesState), bucketOf(order[j].SpacesState)
		if pi, pj := bucketPriority(bi), bucketPriority(bj); pi != pj {
			return pi < pj
		}
		if order[i].SessionName != order[j].SessionName {
			return order[i].SessionName < order[j].SessionName
		}
		if order[i].WindowIndex != order[j].WindowIndex {
			return order[i].WindowIndex < order[j].WindowIndex
		}
		return order[i].NumericID < order[j].NumericID
	})
	for _, p := range order {
		switch bucketOf(p.SpacesState) {
		case BucketNeeds, BucketFailed, BucketDone:
			if best < 0 {
				bestPane = p
				best = bucketPriority(bucketOf(p.SpacesState))
			}
		}
	}
	if best < 0 {
		return tmux.Pane{}, "", false
	}
	target := bestPane.SessionName + ":" + bestPane.WindowID + "." + bestPane.ID
	return bestPane, target, true
}

// cmdNext jumps to the highest-priority attention pane.
func cmdNext(args []string, env Env) int {
	f, err := parseFlags(args, nil, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces next\n")
			return ExitOK
		}
		env.errf("tmux-spaces next: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) > 0 {
		env.errf("usage: tmux-spaces next\n")
		return ExitUsage
	}
	client, cerr := env.requireClient()
	if cerr != nil {
		env.errf("tmux-spaces next: %s\n", cerr)
		return ExitError
	}
	snap, serr := env.loadSpaces()
	if serr != nil {
		env.errf("tmux-spaces next: %s\n", serr)
		return ExitError
	}
	_, target, ok := nextTarget(snap)
	if !ok {
		env.errf("tmux-spaces: no attention panes\n")
		return ExitOK
	}
	if serr := client.SwitchTo(target); serr != nil {
		env.errf("tmux-spaces next: %s\n", serr)
		return ExitError
	}
	env.outf("%s\n", target)
	return ExitOK
}
