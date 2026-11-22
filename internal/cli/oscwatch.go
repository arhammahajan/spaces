package cli

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/osc"
)

const oscDedupeWindow = 15 * time.Second

// cmdOSCWatch implements `tmux-spaces __osc-watch [PANE_ID]`, the helper
// attached via `tmux pipe-pane -o` to observe raw OSC 7501 program-status
// sequences emitted by harnesses directly inside tmux panes.
//
// Each parsed OSC 7501 record is projected onto the pane's native `@spaces_*`
// options (`@spaces_state`, `@spaces_app`, `@spaces_kind`, `@spaces_msg`,
// `@spaces_ts`, `@spaces_source=osc7501`). Exits 0 on EOF when the pane closes.
//
// Stdin is drained continuously on the reader goroutine while tmux option
// updates run asynchronously in a coalescing worker so `pipe-pane` can never
// backpressure a harness's PTY output or stall TUI rendering.
func cmdOSCWatch(args []string, env Env) int {
	f, err := parseFlags(args, nil, nil, nil)
	if err != nil {
		if err == errHelp {
			env.outf("usage: tmux-spaces __osc-watch [PANE_ID]\n")
			return ExitOK
		}
		env.errf("tmux-spaces __osc-watch: %s\n", err)
		return ExitUsage
	}
	pane := ""
	if len(f.pos) > 0 {
		pane = strings.TrimSpace(f.pos[0])
	}
	if pane == "" {
		pane = strings.TrimSpace(env.Getenv("TMUX_PANE"))
	}
	if pane == "" {
		env.errf("tmux-spaces __osc-watch: pane id required (pass PANE_ID or set TMUX_PANE)\n")
		return ExitUsage
	}
	client, err := env.requireClient()
	if err != nil {
		env.errf("tmux-spaces __osc-watch: %s\n", err)
		return ExitError
	}

	type pendingRec struct {
		rec osc.Record
		at  time.Time
	}

	var (
		mu      sync.Mutex
		cond    = sync.NewCond(&mu)
		hasNext bool
		next    pendingRec
		done    bool
	)

	var (
		lastKey   string
		lastState string
		lastAt    time.Time
		applied   = map[string]string{}
		known     = map[string]bool{}
	)

	setIfChanged := func(opt, val string) {
		if known[opt] && applied[opt] == val {
			return
		}
		if err := client.SetPaneOption(pane, opt, val); err == nil {
			applied[opt] = val
			known[opt] = true
		}
	}
	unsetIfSet := func(opt string) {
		if known[opt] && applied[opt] == "" {
			return
		}
		if err := client.UnsetPaneOption(pane, opt); err == nil {
			applied[opt] = ""
			known[opt] = true
		}
	}

	apply := func(rec osc.Record, now time.Time) {
		key := rec.State + "\x00" + rec.App + "\x00" + rec.Profile + "\x00" + rec.Kind + "\x00" + rec.Msg
		if key == lastKey && !lastAt.IsZero() && now.Sub(lastAt) < oscDedupeWindow {
			return
		}
		lastKey = key
		lastAt = now

		if rec.State == "clear" {
			lastState = ""
			for _, opt := range []string{
				"@spaces_state", "@spaces_app", "@spaces_profile", "@spaces_kind",
				"@spaces_msg", "@spaces_ts", "@spaces_source", "@spaces_title",
				"@spaces_resume_session", "@spaces_resume_agent", "@spaces_prior_outcome",
			} {
				_ = client.UnsetPaneOption(pane, opt)
				applied[opt] = ""
				known[opt] = true
			}
			return
		}

		prevState := lastState
		lastState = rec.State

		// Always write state, timestamp, and source when apply runs (already
		// rate-limited by oscDedupeWindow) so out-of-band updates from hooks
		// or `tmux-spaces status` never leave `@spaces_state` stale.
		if err := client.SetPaneOption(pane, "@spaces_state", rec.State); err == nil {
			applied["@spaces_state"] = rec.State
			known["@spaces_state"] = true
		}
		if rec.App != "" {
			newApp := canonicalApp(rec.App)
			if rec.Profile == "" && known["@spaces_app"] && applied["@spaces_app"] != "" && applied["@spaces_app"] != newApp {
				unsetIfSet("@spaces_profile")
			}
			setIfChanged("@spaces_app", newApp)
		}
		if rec.Profile != "" {
			setIfChanged("@spaces_profile", rec.Profile)
		}
		if rec.Kind != "" {
			setIfChanged("@spaces_kind", rec.Kind)
		} else {
			unsetIfSet("@spaces_kind")
		}
		if rec.Msg != "" {
			setIfChanged("@spaces_msg", rec.Msg)
		} else {
			unsetIfSet("@spaces_msg")
		}
		ts := strconv.FormatInt(now.UnixMilli(), 10)
		if err := client.SetPaneOption(pane, "@spaces_ts", ts); err == nil {
			applied["@spaces_ts"] = ts
			known["@spaces_ts"] = true
		}
		if err := client.SetPaneOption(pane, "@spaces_source", "osc7501"); err == nil {
			applied["@spaces_source"] = "osc7501"
			known["@spaces_source"] = true
		}

		if env.RecordOutcome != nil {
			outcome := ""
			switch strings.ToLower(strings.TrimSpace(rec.State)) {
			case "done":
				outcome = "done"
			case "error", "failed":
				outcome = "failed"
			}
			prevNorm := strings.ToLower(strings.TrimSpace(prevState))
			if prevNorm == "error" {
				prevNorm = "failed"
			}
			if outcome != "" && prevNorm != outcome {
				app := applied["@spaces_app"]
				if rec.App != "" {
					app = canonicalApp(rec.App)
				}
				if app != "" {
					prof := applied["@spaces_profile"]
					if rec.Profile != "" {
						prof = rec.Profile
					}
					env.RecordOutcome(OutcomeRecord{
						Pane:    pane,
						App:     app,
						Profile: prof,
						Outcome: outcome,
						Text:    rec.Msg,
						TsMs:    now.UnixMilli(),
					})
				}
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			mu.Lock()
			for !hasNext && !done {
				cond.Wait()
			}
			if !hasNext && done {
				mu.Unlock()
				return
			}
			item := next
			hasNext = false
			mu.Unlock()

			apply(item.rec, item.at)
		}
	}()

	watchErr := osc.Watch(env.Stdin, func(rec osc.Record) {
		now := env.Now()
		mu.Lock()
		next = pendingRec{rec: rec, at: now}
		hasNext = true
		cond.Signal()
		mu.Unlock()
	})

	mu.Lock()
	done = true
	cond.Signal()
	mu.Unlock()
	wg.Wait()

	if watchErr != nil {
		env.errf("tmux-spaces __osc-watch: %s\n", watchErr)
		return ExitError
	}
	return ExitOK
}
