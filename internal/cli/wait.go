package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/tmux"
)

// normalizeWaitState maps user-facing state names onto buckets. "any"
// matches any attention-worthy bucket (needs, failed, done).
func normalizeWaitState(want string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(want)) {
	case "needs_you", "needs-you", "needsyou", "blocked", "needs":
		return BucketNeeds, true
	case "failed", "error":
		return BucketFailed, true
	case "done":
		return BucketDone, true
	case "working":
		return BucketWorking, true
	case "idle", "quiet":
		return BucketIdle, true
	case "any":
		return "any", true
	default:
		return "", false
	}
}

// parseWaitTimeout accepts Go durations ("60s", "5m") and bare seconds.
func parseWaitTimeout(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("empty timeout")
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("invalid timeout %q", raw)
}

// snapshotBucket resolves target to a bucket: a pane id ("%12" or "12"),
// or a session name whose panes roll up to their hottest bucket.
// Missing targets report "" so the caller keeps polling.
func snapshotBucket(snap tmux.ServerSnapshot, target string) string {
	for _, p := range snap.Panes {
		if p.ID == target || "%"+strconv.Itoa(p.NumericID) == target || strconv.Itoa(p.NumericID) == target {
			return bucketOf(p.SpacesState)
		}
	}
	hot := -1
	bucket := ""
	for _, p := range snap.Panes {
		if p.SessionName != target {
			continue
		}
		b := bucketOf(p.SpacesState)
		if pr := bucketPriority(b); hot < 0 || pr < hot {
			hot, bucket = pr, b
		}
	}
	return bucket
}

func waitSatisfied(bucket, want string) bool {
	if want == "any" {
		return bucket == BucketNeeds || bucket == BucketFailed || bucket == BucketDone
	}
	return bucket == want
}

// cmdWait polls pane status until the condition holds or the timeout lapses.
func cmdWait(args []string, env Env) int {
	f, err := parseFlags(args, []string{"until", "timeout"}, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces wait <target> [--until STATE] [--timeout DURATION]\n")
			return ExitOK
		}
		env.errf("tmux-spaces wait: %s\n", err)
		return ExitUsage
	}
	if len(f.pos) != 1 {
		env.errf("usage: tmux-spaces wait <target> [--until STATE] [--timeout DURATION]\n")
		return ExitUsage
	}
	target := f.pos[0]
	want := BucketNeeds
	if f.set["until"] {
		var ok bool
		want, ok = normalizeWaitState(f.str["until"])
		if !ok {
			env.errf("tmux-spaces wait: invalid state %q (done, failed, needs_you, idle, working, any)\n", f.str["until"])
			return ExitUsage
		}
	}
	timeout := 60 * time.Second
	if f.set["timeout"] {
		var terr error
		timeout, terr = parseWaitTimeout(f.str["timeout"])
		if terr != nil {
			env.errf("tmux-spaces wait: %s\n", terr)
			return ExitUsage
		}
	}
	deadline := env.Now().Add(timeout)
	// Iteration cap guards against frozen clocks in tests: the deadline
	// check below normally terminates the loop.
	maxAttempts := int(timeout/time.Second)*4 + 120
	last := ""
	for attempt := 0; ; attempt++ {
		snap, serr := env.loadSpaces()
		if serr != nil {
			env.errf("tmux-spaces wait: %s\n", serr)
			return ExitError
		}
		current := snapshotBucket(snap, target)
		if current != "" {
			last = current
		}
		if current != "" && waitSatisfied(current, want) {
			env.outf("%s\n", current)
			return ExitOK
		}
		if attempt >= maxAttempts || !env.Now().Before(deadline) {
			break
		}
		env.Sleep(500 * time.Millisecond)
		// Re-check the clock after sleeping: a stubbed clock advances here.
		if !env.Now().Before(deadline) {
			break
		}
	}
	if last == "" {
		last = "unknown"
	}
	env.errf("tmux-spaces wait: timed out waiting for %s (last: %s)\n", want, last)
	return ExitError
}
