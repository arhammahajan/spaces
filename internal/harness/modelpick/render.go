package modelpick

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// headers are the table's column titles, in order.
var headers = []string{"ID", "CTX", "OUT", "REASONING", "MODALITIES"}

// Render returns the plain aligned table for the listing: the
// ID/CTX/OUT/REASONING/MODALITIES header row plus one row per model,
// sorted by ID, token counts humanized 1M/200K-style, "?" for every
// unknown cell, columns separated and padded to align with two spaces,
// and the last column left unpadded. Headless callers print it to
// stdout. The input slice is never mutated.
func Render(models []Model) string {
	sorted := sortedCopy(models)
	rows := make([][]string, 0, len(sorted)+1)
	rows = append(rows, headers)
	for _, m := range sorted {
		rows = append(rows, modelCells(m))
	}
	return renderRows(rows)
}

// sortedCopy returns a copy of models sorted by ID, leaving the input
// untouched.
func sortedCopy(models []Model) []Model {
	out := append([]Model(nil), models...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// modelCells renders one model's five table cells: humanized token
// counts, the compact reasoning hint, and the input modality list, with
// "?" wherever the router served no fact.
func modelCells(m Model) []string {
	reasoning := m.Reasoning
	if reasoning == "" {
		reasoning = "?"
	}
	mods := "?"
	if len(m.Modalities) > 0 {
		mods = strings.Join(m.Modalities, ",")
	}
	return []string{m.ID, humanTokens(m.ContextWindow), humanTokens(m.MaxOutput), reasoning, mods}
}

// renderRows aligns rows of equal-length cells into a table: every
// column padded to its widest cell, a two-space gutter between columns,
// and the last column unpadded. Each line ends in a newline.
func renderRows(rows [][]string) string {
	widths := make([]int, len(headers))
	for _, row := range rows {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}
	var b strings.Builder
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			if i < len(row)-1 {
				b.WriteString(cell)
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)))
			} else {
				b.WriteString(cell)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// humanTokens renders a token count the way the table does: 1M/200K
// humanization above a thousand (one decimal only when needed), the
// exact number below it, and "?" for the unknown 0.
func humanTokens(n int64) string {
	switch {
	case n == 0:
		return "?"
	case n >= 1_000_000:
		v := float64(n) / 1_000_000
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10) + "M"
		}
		return strconv.FormatFloat(v, 'f', 1, 64) + "M"
	case n >= 1_000:
		v := float64(n) / 1_000
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10) + "K"
		}
		return strconv.FormatFloat(v, 'f', 0, 64) + "K"
	default:
		return strconv.FormatInt(n, 10)
	}
}
