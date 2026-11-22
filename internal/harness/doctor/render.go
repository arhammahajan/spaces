// Rendering: the human STATE/NAME/DETAIL table with its summary line,
// and the typed --json document. `harness doctor` prints Render;
// `harness doctor --json` marshals BuildReport (or prints RenderJSON).

package doctor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Summary counts the checks per state.
type Summary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

// Summarize counts checks by state. Rows in an unknown state are not
// counted.
func Summarize(checks []Check) Summary {
	var s Summary
	for _, c := range checks {
		switch c.State {
		case StateOK:
			s.OK++
		case StateWarn:
			s.Warn++
		case StateFail:
			s.Fail++
		}
	}
	return s
}

// Report is the typed --json shape: every check row plus the summary.
type Report struct {
	Checks  []Check `json:"checks"`
	Summary Summary `json:"summary"`
}

// BuildReport assembles the typed report for checks.
func BuildReport(checks []Check) Report {
	return Report{Checks: checks, Summary: Summarize(checks)}
}

// Render returns the aligned STATE/NAME/DETAIL rows followed by the
// "N ok, M warn, K fail" summary line. The result always ends with a
// newline; the name column is as wide as the longest name so the detail
// column stays aligned.
func Render(checks []Check) string {
	nameWidth := 0
	for _, c := range checks {
		if len(c.Name) > nameWidth {
			nameWidth = len(c.Name)
		}
	}
	var b strings.Builder
	for _, c := range checks {
		line := fmt.Sprintf("%-4s  %-*s  %s", c.State, nameWidth, c.Name, c.Detail)
		if c.Detail == "" {
			line = strings.TrimRight(line, " ")
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	s := Summarize(checks)
	fmt.Fprintf(&b, "%d ok, %d warn, %d fail\n", s.OK, s.Warn, s.Fail)
	return b.String()
}

// RenderJSON renders the typed --json document, indented for humans and
// ending with a newline.
func RenderJSON(checks []Check) (string, error) {
	data, err := json.MarshalIndent(BuildReport(checks), "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}
