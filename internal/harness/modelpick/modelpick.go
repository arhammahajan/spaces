// Package modelpick turns the router's GET /v1/models listing into the
// interactive `harness models` chooser.
//
// Fetch performs its own /v1/models request with Bearer auth using
// routerctl.Options' Router URL and API key, and parses id, owned_by,
// and the optional metadata.router facts —
// limits.context_window.value, limits.max_output_tokens.value, a
// compact reasoning hint, the modalities input list, and
// freshness.discovery.stale — tolerating every absence and null: the
// live payload serves nulls liberally (value null, kind unknown, whole
// keys missing), so unknown facts stay at their zero values instead of
// failing the listing.
//
// Render returns the plain aligned ID/CTX/OUT/REASONING/MODALITIES
// table (1M/200K humanized token counts, "?" for unknown, sorted by ID)
// that headless callers print to stdout. Pick adds the interactive
// layer — a numbered table and a single prompt on stderr, gated on the
// StdinIsTerminal seam — and is a no-op without a terminal.
package modelpick

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Model is one GET /v1/models entry with the router facts the picker
// shows. Every optional fact tolerates absence: unknown limits are 0,
// an unknown reasoning hint is "", unknown modalities are nil, and
// unknown freshness is not stale.
type Model struct {
	ID            string   // row id
	OwnedBy       string   // row owned_by
	ContextWindow int64    // metadata.router.limits.context_window.value; 0 = unknown
	MaxOutput     int64    // metadata.router.limits.max_output_tokens.value; 0 = unknown
	Reasoning     string   // compact hint from metadata.router.reasoning; "" = unknown
	Modalities    []string // metadata.router.modalities input list (facts that are true)
	Stale         bool     // metadata.router.freshness.discovery.stale
}

// Injectable seams, in the zjcli style. Tests replace these; production
// uses the defaults.
var (
	// Stdin is the interactive picker's input stream.
	Stdin io.Reader = os.Stdin
	// Stderr receives the numbered table and the prompt, so interactive
	// output never pollutes the stdout that headless callers use for
	// the plain Render table.
	Stderr io.Writer = os.Stderr
	// StdinIsTerminal gates the interactive picker: without a terminal
	// there is no one to ask and Pick is a no-op.
	StdinIsTerminal = func() bool {
		fi, err := os.Stdin.Stat()
		if err != nil {
			return false
		}
		return fi.Mode()&os.ModeCharDevice != 0
	}
)

// Pick offers the numbered table on the terminal and returns the chosen
// model. The prompt accepts a number; any other non-empty input filters
// the full listing by a case-insensitive substring over ID and
// re-renders the renumbered results with the same prompt; empty input
// picks the first listed model; q cancels. All output goes to Stderr.
//
// Pick is a no-op returning false without printing when the listing is
// empty or stdin is not a terminal (StdinIsTerminal), so headless
// callers print Render instead. A closed input also cancels rather than
// guessing.
func Pick(models []Model) (Model, bool) {
	if len(models) == 0 || !StdinIsTerminal() {
		return Model{}, false
	}
	all := sortedCopy(models)
	shown := all
	fmt.Fprint(Stderr, numberedTable(shown))
	reader := bufio.NewReader(Stdin)
	for {
		fmt.Fprintf(Stderr, "Choice [1-%d, filter, Enter=first, q=quit]: ", len(shown))
		line, readErr := reader.ReadString('\n')
		input := strings.TrimSpace(line)
		if input == "" {
			// EOF with nothing typed cancels rather than guessing,
			// mirroring the access picker; Enter picks the first
			// listed model unless nothing is listed.
			if (readErr != nil && line == "") || len(shown) == 0 {
				return Model{}, false
			}
			return shown[0], true
		}
		if strings.EqualFold(input, "q") {
			return Model{}, false
		}
		if n, err := strconv.Atoi(input); err == nil && n >= 1 && n <= len(shown) {
			return shown[n-1], true
		}
		// Any other input — including a number outside the listing —
		// filters and re-renders, always over the full listing so a
		// dead-end filter is recoverable with the next substring.
		shown = filterModels(all, input)
		fmt.Fprint(Stderr, numberedTable(shown))
	}
}

// filterModels selects the models whose ID contains needle, ignoring
// case.
func filterModels(models []Model, needle string) []Model {
	needle = strings.ToLower(needle)
	var out []Model
	for _, m := range models {
		if strings.Contains(strings.ToLower(m.ID), needle) {
			out = append(out, m)
		}
	}
	return out
}

// numberedTable renders the listing as the aligned picker table with a
// 1-based choice number before each ID.
func numberedTable(models []Model) string {
	rows := make([][]string, 0, len(models)+1)
	rows = append(rows, headers)
	for i, m := range models {
		cells := modelCells(m)
		cells[0] = strconv.Itoa(i+1) + ") " + cells[0]
		rows = append(rows, cells)
	}
	return renderRows(rows)
}
