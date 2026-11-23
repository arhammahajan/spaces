package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/usage"
)

// usageRange selects the historical time window shown in the Usage popup.
type usageRange int

const (
	usageRange7d usageRange = iota
	usageRange30d
	usageRange90d
	usageRangeAll
)

func (r usageRange) label() string {
	switch r {
	case usageRange7d:
		return "7d"
	case usageRange30d:
		return "30d"
	case usageRange90d:
		return "90d"
	default:
		return "all"
	}
}

func (r usageRange) days() int {
	switch r {
	case usageRange7d:
		return 7
	case usageRange30d:
		return 30
	case usageRange90d:
		return 90
	default:
		return 0
	}
}

func (r usageRange) next() usageRange {
	return (r + 1) % 4
}

// usageOverviewAgg selects the 52-week activity chart mode on the Overview tab.
type usageOverviewAgg int

const (
	usageAggDaily usageOverviewAgg = iota
	usageAggWeekly
	usageAggCumulative
)

func (a usageOverviewAgg) label() string {
	switch a {
	case usageAggWeekly:
		return "Weekly"
	case usageAggCumulative:
		return "Cumulative"
	default:
		return "Daily"
	}
}

func (a usageOverviewAgg) next() usageOverviewAgg {
	return (a + 1) % 3
}

// usageGroupBy selects the series stacking dimension on the Usage tab.
type usageGroupBy int

const (
	usageGroupProvider usageGroupBy = iota
	usageGroupModel
	usageGroupProject
	usageGroupTokenType
)

func (g usageGroupBy) label() string {
	switch g {
	case usageGroupModel:
		return "Model"
	case usageGroupProject:
		return "Project"
	case usageGroupTokenType:
		return "Token type"
	default:
		return "Provider"
	}
}

func (g usageGroupBy) next() usageGroupBy {
	return (g + 1) % 4
}

// usageSessionSort selects the sort order on the Sessions tab.
type usageSessionSort int

const (
	usageSortCost usageSessionSort = iota
	usageSortTokens
	usageSortRecent
)

func (s usageSessionSort) label() string {
	switch s {
	case usageSortTokens:
		return "Tokens"
	case usageSortRecent:
		return "Recent"
	default:
		return "Cost"
	}
}

func (s usageSessionSort) next() usageSessionSort {
	return (s + 1) % 3
}

// usageSeriesItem describes one series slice in the stacked bar breakdown.
type usageSeriesItem struct {
	Label   string
	Color   string
	Tokens  int64
	CostUSD float64
	Share   float64
}

// usageTabHit records the horizontal cell bounds of a rendered tab for mouse clicks.
type usageTabHit struct {
	Index int
	Start int
	End   int // exclusive
}

var (
	usageSeriesPalette = [4]string{ColorBlue, ColorMauve, ColorGreen, ColorOverlay0}
	usageLowerBlocks   = [9]rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	usageHeatmapRamp   = [5]string{ColorSurface1, ColorBlue, ColorLavender, ColorTeal, ColorGreen}
)

// renderUsageTabBar renders the Codex-styled filled chip tab bar with overflow chevrons.
func renderUsageTabBar(tabs []string, active int, width int) string {
	line, _ := usageTabBarLayout(tabs, active, width)
	return line
}

func usageTabBarLayout(tabs []string, active int, width int) (string, []usageTabHit) {
	if len(tabs) == 0 || width <= 0 {
		return "", nil
	}
	if active < 0 {
		active = 0
	}
	if active >= len(tabs) {
		active = len(tabs) - 1
	}

	numbered := width >= 80
	labels := make([]string, len(tabs))
	rawWidths := make([]int, len(tabs))
	for i, name := range tabs {
		if numbered {
			labels[i] = fmt.Sprintf(" %d %s ", i+1, name)
		} else {
			labels[i] = fmt.Sprintf(" %s ", name)
		}
		rawWidths[i] = ansi.StringWidth(labels[i])
	}

	// Determine visible slice [start, end) that always includes active.
	start, end := 0, len(tabs)
	totalW := func(s, e int) int {
		w := 0
		for i := s; i < e; i++ {
			if i > s {
				w++ // 1-space gap between tabs
			}
			w += rawWidths[i]
		}
		if s > 0 {
			w += 2 // "‹ "
		}
		if e < len(tabs) {
			w += 2 // " ›"
		}
		return w
	}

	if totalW(start, end) > width {
		start = active
		end = active + 1
		for {
			expanded := false
			if end < len(tabs) && totalW(start, end+1) <= width {
				end++
				expanded = true
			}
			if start > 0 && totalW(start-1, end) <= width {
				start--
				expanded = true
			}
			if !expanded {
				break
			}
		}
	}

	activeSt := lipgloss.NewStyle().
		Background(lipgloss.Color(ColorSurface1)).
		Foreground(lipgloss.Color(ColorLavender)).
		Bold(true)
	inactiveSt := lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorOverlay0))
	chevronSt := lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorOverlay0))

	var b strings.Builder
	var hits []usageTabHit
	col := 0

	if start > 0 {
		b.WriteString(chevronSt.Render("‹ "))
		col += 2
	}
	for i := start; i < end; i++ {
		if i > start {
			b.WriteByte(' ')
			col++
		}
		w := rawWidths[i]
		if i == active {
			b.WriteString(activeSt.Render(labels[i]))
		} else {
			b.WriteString(inactiveSt.Render(labels[i]))
		}
		hits = append(hits, usageTabHit{Index: i, Start: col, End: col + w})
		col += w
	}
	if end < len(tabs) {
		b.WriteString(chevronSt.Render(" ›"))
	}

	return fitCellWidth(b.String(), width), hits
}

// renderActivityHeatmap renders the 52-week Token Activity chart in Daily, Weekly, or Cumulative mode.
func renderActivityHeatmap(daily []usage.DailyBucket, aggMode usageOverviewAgg, width int, now time.Time) []string {
	if width < 20 {
		width = 20
	}
	if now.IsZero() {
		now = time.Now()
	}

	// Build a date -> tokens lookup map.
	byDate := make(map[string]int64, len(daily))
	for _, d := range daily {
		byDate[d.Date] += d.Totals.Total()
	}

	// Compute the 52-week window ending on the Saturday of `now`'s week.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	daysUntilSat := int(time.Saturday - today.Weekday())
	endSat := today.AddDate(0, 0, daysUntilSat)

	// Decide how many weeks fit cleanly.
	gutterW := 4 // "Su  " or max label
	if aggMode != usageAggDaily {
		gutterW = 7 // e.g. "12.4M "
	}
	availCols := width - gutterW
	if availCols < 12 {
		availCols = 12
	}

	weeks := 52
	cellStep := 1
	if aggMode == usageAggDaily && availCols >= 52*2 {
		cellStep = 2
	} else if availCols < weeks {
		weeks = availCols
	}

	startSun := endSat.AddDate(0, 0, -(weeks*7 - 1))

	// Populate grid[week][weekday] and weekly totals.
	grid := make([][7]int64, weeks)
	weekTotals := make([]int64, weeks)
	var maxDaily int64
	for w := 0; w < weeks; w++ {
		for dow := 0; dow < 7; dow++ {
			dt := startSun.AddDate(0, 0, w*7+dow)
			if dt.After(today) {
				continue
			}
			tok := byDate[dt.Format("2006-01-02")]
			grid[w][dow] = tok
			weekTotals[w] += tok
			if tok > maxDaily {
				maxDaily = tok
			}
		}
	}

	switch aggMode {
	case usageAggWeekly, usageAggCumulative:
		vals := make([]int64, weeks)
		var peak int64
		var running int64
		for w := 0; w < weeks; w++ {
			if aggMode == usageAggCumulative {
				running += weekTotals[w]
				vals[w] = running
			} else {
				vals[w] = weekTotals[w]
			}
			if vals[w] > peak {
				peak = vals[w]
			}
		}
		return renderActivityColumnBars(vals, peak, startSun, aggMode, width)
	default:
		return renderActivityDailyGrid(grid, maxDaily, startSun, today, cellStep, width)
	}
}

func renderActivityDailyGrid(grid [][7]int64, maxDaily int64, startSun, today time.Time, cellStep, width int) []string {
	weeks := len(grid)
	lines := make([]string, 0, 10)

	// Month header line aligned above week columns.
	headerCells := make([]rune, weeks*cellStep)
	for i := range headerCells {
		headerCells[i] = ' '
	}
	lastMonth := time.Month(0)
	lastLabelEnd := -1
	for w := 0; w < weeks; w++ {
		dt := startSun.AddDate(0, 0, w*7)
		m := dt.Month()
		if w == 0 || m != lastMonth {
			pos := w * cellStep
			if pos >= lastLabelEnd+1 && pos+3 <= len(headerCells) {
				lbl := dt.Format("Jan")
				for k, r := range lbl {
					headerCells[pos+k] = r
				}
				lastLabelEnd = pos + 3
			}
			lastMonth = m
		}
	}
	lines = append(lines, fitCellWidth(styleMuted.Render("    "+string(headerCells)), width))

	dayLabels := [7]string{"Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"}
	emptySt := lipgloss.NewStyle().Foreground(lipgloss.Color(usageHeatmapRamp[0]))
	lvlSt := [5]lipgloss.Style{
		emptySt,
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageHeatmapRamp[1])),
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageHeatmapRamp[2])),
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageHeatmapRamp[3])),
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageHeatmapRamp[4])),
	}

	for dow := 0; dow < 7; dow++ {
		var row strings.Builder
		row.WriteString(styleMuted.Render(dayLabels[dow] + "  "))
		for w := 0; w < weeks; w++ {
			dt := startSun.AddDate(0, 0, w*7+dow)
			if dt.After(today) {
				if cellStep == 2 {
					row.WriteString("  ")
				} else {
					row.WriteByte(' ')
				}
				continue
			}
			tok := grid[w][dow]
			lvl := heatmapIntensityLevel(tok, maxDaily)
			if lvl == 0 {
				row.WriteString(emptySt.Render("□"))
			} else {
				row.WriteString(lvlSt[lvl].Render("■"))
			}
			if cellStep == 2 {
				row.WriteByte(' ')
			}
		}
		lines = append(lines, fitCellWidth(row.String(), width))
	}

	// Legend line: Less □ ■ ■ ■ ■ More
	legend := styleMuted.Render("    Less ") +
		emptySt.Render("□") + " " +
		lvlSt[1].Render("■") + " " +
		lvlSt[2].Render("■") + " " +
		lvlSt[3].Render("■") + " " +
		lvlSt[4].Render("■") +
		styleMuted.Render(" More")
	lines = append(lines, fitCellWidth(legend, width))
	return lines
}

func heatmapIntensityLevel(val, maxVal int64) int {
	if val <= 0 || maxVal <= 0 {
		return 0
	}
	ratio := float64(val) / float64(maxVal)
	switch {
	case ratio <= 0.15:
		return 1
	case ratio <= 0.40:
		return 2
	case ratio <= 0.70:
		return 3
	default:
		return 4
	}
}

func renderActivityColumnBars(vals []int64, peak int64, startSun time.Time, aggMode usageOverviewAgg, width int) []string {
	weeks := len(vals)
	const rows = 7
	gutterW := 7
	avail := width - gutterW
	if avail < weeks {
		weeks = avail
		if weeks < 1 {
			weeks = 1
		}
		vals = vals[len(vals)-weeks:]
		startSun = startSun.AddDate(0, 0, (len(vals)-weeks)*7)
	}

	barColor := ColorBlue
	if aggMode == usageAggCumulative {
		barColor = ColorTeal
	}
	barSt := lipgloss.NewStyle().Foreground(lipgloss.Color(barColor))
	emptySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface0))

	lines := make([]string, 0, rows+3)

	// Month header line
	headerCells := make([]rune, weeks)
	for i := range headerCells {
		headerCells[i] = ' '
	}
	lastMonth := time.Month(0)
	lastEnd := -1
	for w := 0; w < weeks; w++ {
		dt := startSun.AddDate(0, 0, w*7)
		if w == 0 || dt.Month() != lastMonth {
			if w >= lastEnd+1 && w+3 <= weeks {
				lbl := dt.Format("Jan")
				for k, r := range lbl {
					headerCells[w+k] = r
				}
				lastEnd = w + 3
			}
			lastMonth = dt.Month()
		}
	}
	lines = append(lines, fitCellWidth(styleMuted.Render(strings.Repeat(" ", gutterW)+string(headerCells)), width))

	peakLabel := formatCompactTokens(peak)
	midLabel := formatCompactTokens(peak / 2)

	for r := rows - 1; r >= 0; r-- {
		var gutter string
		switch r {
		case rows - 1:
			gutter = fmt.Sprintf("%*s ", gutterW-1, peakLabel)
		case rows / 2:
			gutter = fmt.Sprintf("%*s ", gutterW-1, midLabel)
		case 0:
			gutter = fmt.Sprintf("%*s ", gutterW-1, "0")
		default:
			gutter = strings.Repeat(" ", gutterW)
		}
		var b strings.Builder
		b.WriteString(styleMuted.Render(gutter))
		for w := 0; w < weeks; w++ {
			if peak <= 0 || vals[w] <= 0 {
				if r == 0 {
					b.WriteString(emptySt.Render("▁"))
				} else {
					b.WriteByte(' ')
				}
				continue
			}
			scaled := float64(vals[w]) / float64(peak) * float64(rows)
			cell := scaled - float64(r)
			switch {
			case cell >= 1.0:
				b.WriteString(barSt.Render("█"))
			case cell <= 0:
				b.WriteByte(' ')
			default:
				idx := int(math.Round(cell * 8))
				if idx < 1 {
					idx = 1
				}
				if idx > 8 {
					idx = 8
				}
				b.WriteString(barSt.Render(string(usageLowerBlocks[idx])))
			}
		}
		lines = append(lines, fitCellWidth(b.String(), width))
	}

	var caption string
	if aggMode == usageAggCumulative {
		caption = fmt.Sprintf("Running total · top %s", formatCompactTokens(peak))
	} else {
		caption = fmt.Sprintf("Each column = 1 week · tallest %s", formatCompactTokens(peak))
	}
	lines = append(lines, fitCellWidth(styleMuted.Render(strings.Repeat(" ", gutterW)+caption), width))
	return lines
}

// renderStackedBarChart renders a Codex-styled stacked vertical bar chart using 8-level Unicode lower blocks.
// Returns the rendered lines and the breakdown series items for the selected day.
func renderStackedBarChart(days []usage.DailyBucket, selectedIdx int, groupBy usageGroupBy, useCost bool, width, height int) ([]string, []usageSeriesItem) {
	if width < 24 {
		width = 24
	}
	if height < 6 {
		height = 6
	}
	if len(days) == 0 {
		return []string{fitCellWidth(styleMuted.Render("No daily usage data in range."), width)}, nil
	}
	if selectedIdx < 0 || selectedIdx >= len(days) {
		selectedIdx = len(days) - 1
	}

	// 1. Collect global top-3 category keys across all visible days; 4th slot is "Other".
	globalTotals := map[string]float64{}
	for _, d := range days {
		for k, v := range bucketCategoryValues(d, groupBy, useCost) {
			globalTotals[k] += v
		}
	}
	type kv struct {
		k string
		v float64
	}
	ranked := make([]kv, 0, len(globalTotals))
	for k, v := range globalTotals {
		if v > 0 {
			ranked = append(ranked, kv{k: k, v: v})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].v != ranked[j].v {
			return ranked[i].v > ranked[j].v
		}
		return ranked[i].k < ranked[j].k
	})

	topKeys := make([]string, 0, 4)
	topIndex := map[string]int{}
	hasOther := len(ranked) > 3
	limit := len(ranked)
	if limit > 3 {
		limit = 3
	}
	for i := 0; i < limit; i++ {
		topKeys = append(topKeys, ranked[i].k)
		topIndex[ranked[i].k] = i
	}
	if hasOther {
		topKeys = append(topKeys, "Other")
	}

	// Build breakdown series for the selected day.
	selDay := days[selectedIdx]
	selCostMap := bucketCategoryValues(selDay, groupBy, true)
	selTokMap := bucketCategoryValues(selDay, groupBy, false)
	series := make([]usageSeriesItem, len(topKeys))
	for i, lbl := range topKeys {
		series[i] = usageSeriesItem{Label: lbl, Color: usageSeriesPalette[i%len(usageSeriesPalette)]}
	}
	var selTotalMetric float64
	for k, cv := range selCostMap {
		tv := int64(selTokMap[k])
		slot, ok := topIndex[k]
		if !ok {
			if hasOther {
				slot = 3
			} else {
				continue
			}
		}
		series[slot].CostUSD += cv
		series[slot].Tokens += tv
	}
	// Also include token-only entries not in selCostMap (e.g. unpriced models).
	for k, tv := range selTokMap {
		if _, seen := selCostMap[k]; seen {
			continue
		}
		slot, ok := topIndex[k]
		if !ok {
			if hasOther {
				slot = 3
			} else {
				continue
			}
		}
		series[slot].Tokens += int64(tv)
	}
	for i := range series {
		if useCost {
			selTotalMetric += series[i].CostUSD
		} else {
			selTotalMetric += float64(series[i].Tokens)
		}
	}
	for i := range series {
		if selTotalMetric > 0 {
			if useCost {
				series[i].Share = (series[i].CostUSD / selTotalMetric) * 100
			} else {
				series[i].Share = (float64(series[i].Tokens) / selTotalMetric) * 100
			}
		}
	}

	// 2. Determine visible window of days that fits in width.
	gutterW := 8
	plotW := width - gutterW
	if plotW < 10 {
		plotW = 10
	}

	// Compute bar width + gap.
	visDays := days
	visSel := selectedIdx
	maxBars := plotW
	if len(visDays) > maxBars {
		// Center window around selectedIdx.
		start := selectedIdx - maxBars/2
		if start < 0 {
			start = 0
		}
		if start+maxBars > len(days) {
			start = len(days) - maxBars
		}
		visDays = days[start : start+maxBars]
		visSel = selectedIdx - start
	}

	n := len(visDays)
	barW := 1
	gap := 0
	if n > 0 {
		slotW := plotW / n
		switch {
		case slotW >= 5:
			barW = 3
			gap = 1
		case slotW >= 3:
			barW = 2
			gap = 1
		case slotW >= 2:
			barW = 1
			gap = 1
		default:
			barW = 1
			gap = 0
		}
	}
	step := barW + gap

	// Compute per-day stacked slot values and peak.
	daySlots := make([][4]float64, n)
	dayTotals := make([]float64, n)
	var peak float64
	for i, d := range visDays {
		vals := bucketCategoryValues(d, groupBy, useCost)
		var tot float64
		for k, v := range vals {
			if v <= 0 {
				continue
			}
			slot, ok := topIndex[k]
			if !ok {
				if hasOther {
					slot = 3
				} else {
					continue
				}
			}
			daySlots[i][slot] += v
			tot += v
		}
		dayTotals[i] = tot
		if tot > peak {
			peak = tot
		}
	}

	// Reserve 1 row for callout, 1 row for baseline, 1 row for date axis, 1 row for selection cursor.
	plotH := height - 4
	if plotH < 3 {
		plotH = 3
	}

	formatAxis := func(v float64) string {
		if useCost {
			return formatUSD(v)
		}
		return formatCompactTokens(int64(v))
	}

	lines := make([]string, 0, plotH+4)

	// Top callout row: show compact value above selected bar (and peak bar if distinct).
	callout := make([]rune, n*step)
	for i := range callout {
		callout[i] = ' '
	}
	placeCallout := func(idx int, text string) {
		if idx < 0 || idx >= n {
			return
		}
		center := idx*step + barW/2
		rs := []rune(text)
		start := center - len(rs)/2
		if start < 0 {
			start = 0
		}
		if start+len(rs) > len(callout) {
			start = len(callout) - len(rs)
		}
		if start < 0 {
			return
		}
		for k, r := range rs {
			if start+k < len(callout) {
				callout[start+k] = r
			}
		}
	}
	if visSel >= 0 && visSel < n && dayTotals[visSel] > 0 {
		placeCallout(visSel, formatAxis(dayTotals[visSel]))
	}
	lines = append(lines, fitCellWidth(styleAccent.Render(strings.Repeat(" ", gutterW)+string(callout)), width))

	// Plot rows from top (plotH-1) down to 0.
	slotStyles := [4]lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[0])),
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[1])),
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[2])),
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[3])),
	}
	selSlotStyles := [4]lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[0])).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[1])).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[2])).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[3])).Bold(true),
	}

	for r := plotH - 1; r >= 0; r-- {
		var gutter string
		switch r {
		case plotH - 1:
			gutter = fmt.Sprintf("%*s ", gutterW-1, formatAxis(peak))
		case plotH / 2:
			gutter = fmt.Sprintf("%*s ", gutterW-1, formatAxis(peak/2))
		default:
			gutter = strings.Repeat(" ", gutterW)
		}
		var b strings.Builder
		b.WriteString(styleMuted.Render(gutter))

		for i := 0; i < n; i++ {
			tot := dayTotals[i]
			if peak <= 0 || tot <= 0 {
				b.WriteString(strings.Repeat(" ", barW))
			} else {
				totalH := (tot / peak) * float64(plotH)
				cellFill := totalH - float64(r)
				if cellFill <= 0 {
					b.WriteString(strings.Repeat(" ", barW))
				} else {
					// Determine which stacked slot occupies the midpoint of this row.
					rowMidVal := ((float64(r) + 0.5) / float64(plotH)) * peak
					if rowMidVal > tot {
						rowMidVal = tot
					}
					chosenSlot := 0
					var cum float64
					for s := 0; s < 4; s++ {
						cum += daySlots[i][s]
						if rowMidVal <= cum || s == 3 {
							chosenSlot = s
							break
						}
					}
					ch := '█'
					if cellFill < 1.0 {
						idx := int(math.Round(cellFill * 8))
						if idx < 1 {
							idx = 1
						}
						if idx > 8 {
							idx = 8
						}
						ch = usageLowerBlocks[idx]
					}
					seg := strings.Repeat(string(ch), barW)
					if i == visSel {
						b.WriteString(selSlotStyles[chosenSlot].Render(seg))
					} else {
						b.WriteString(slotStyles[chosenSlot].Render(seg))
					}
				}
			}
			if gap > 0 && i < n-1 {
				b.WriteString(strings.Repeat(" ", gap))
			}
		}
		lines = append(lines, fitCellWidth(b.String(), width))
	}

	// Baseline: "     0 ───────"
	baseW := n * step
	if baseW < 1 {
		baseW = 1
	}
	baseline := styleMuted.Render(fmt.Sprintf("%*s ", gutterW-1, "0")) +
		styleDivider.Render(strings.Repeat("─", baseW))
	lines = append(lines, fitCellWidth(baseline, width))

	// Date axis row: first, middle, last date labels.
	axisCells := make([]rune, baseW)
	for i := range axisCells {
		axisCells[i] = ' '
	}
	placeDate := func(idx int) {
		if idx < 0 || idx >= n {
			return
		}
		lbl := shortDateLabel(visDays[idx].Date)
		rs := []rune(lbl)
		pos := idx * step
		if pos+len(rs) > len(axisCells) {
			pos = len(axisCells) - len(rs)
		}
		if pos < 0 {
			return
		}
		for k, r := range rs {
			axisCells[pos+k] = r
		}
	}
	placeDate(0)
	if n >= 5 {
		placeDate(n / 2)
	}
	if n > 1 {
		placeDate(n - 1)
	}
	lines = append(lines, fitCellWidth(styleMuted.Render(strings.Repeat(" ", gutterW)+string(axisCells)), width))

	// Selection cursor row: ▲ under visSel.
	cursorPos := visSel*step + barW/2
	if cursorPos < 0 {
		cursorPos = 0
	}
	cursorLine := strings.Repeat(" ", gutterW+cursorPos) + styleAccent.Render("▲")
	lines = append(lines, fitCellWidth(cursorLine, width))

	return lines, series
}

func bucketCategoryValues(d usage.DailyBucket, groupBy usageGroupBy, useCost bool) map[string]float64 {
	out := map[string]float64{}
	switch groupBy {
	case usageGroupProvider:
		for k, m := range d.ByProvider {
			if useCost {
				out[k] += m.CostUSD
			} else {
				out[k] += float64(m.Tokens)
			}
		}
		if len(out) == 0 {
			if useCost {
				out["Total"] = d.CostUSD
			} else {
				out["Total"] = float64(d.Totals.Total())
			}
		}
	case usageGroupModel:
		for k, m := range d.ByModel {
			if useCost {
				out[k] += m.CostUSD
			} else {
				out[k] += float64(m.Tokens)
			}
		}
		if len(out) == 0 {
			if useCost {
				out["Unknown"] = d.CostUSD
			} else {
				out["Unknown"] = float64(d.Totals.Total())
			}
		}
	case usageGroupProject:
		for k, m := range d.ByProject {
			if useCost {
				out[k] += m.CostUSD
			} else {
				out[k] += float64(m.Tokens)
			}
		}
		if len(out) == 0 {
			if useCost {
				out["(no project)"] = d.CostUSD
			} else {
				out["(no project)"] = float64(d.Totals.Total())
			}
		}
	case usageGroupTokenType:
		if useCost {
			if d.CategoryCost.Input > 0 {
				out["Input"] = d.CategoryCost.Input
			}
			if d.CategoryCost.CacheRead > 0 {
				out["Cache Read"] = d.CategoryCost.CacheRead
			}
			if d.CategoryCost.CacheWrite > 0 {
				out["Cache Write"] = d.CategoryCost.CacheWrite
			}
			if d.CategoryCost.Output > 0 {
				out["Output"] = d.CategoryCost.Output
			}
			if len(out) == 0 && d.CostUSD > 0 {
				out["Total"] = d.CostUSD
			}
		} else {
			if d.Totals.UncachedInputTokens > 0 {
				out["Input"] = float64(d.Totals.UncachedInputTokens)
			}
			if d.Totals.CachedInputTokens > 0 {
				out["Cache Read"] = float64(d.Totals.CachedInputTokens)
			}
			if d.Totals.CacheCreationTokens > 0 {
				out["Cache Write"] = float64(d.Totals.CacheCreationTokens)
			}
			if d.Totals.OutputTokens > 0 {
				out["Output"] = float64(d.Totals.OutputTokens)
			}
		}
	}
	return out
}

func shortDateLabel(isoDate string) string {
	if t, err := time.Parse("2006-01-02", isoDate); err == nil {
		return t.Format("Jan 2")
	}
	if len(isoDate) >= 10 {
		return isoDate[5:10]
	}
	return isoDate
}

// renderCodexQuotaBar renders the Codex `/status` progress bar `[███████████░░░░░░░░░]`
// representing remaining percentage (`100 - usedPct`), colored by remaining capacity.
func renderCodexQuotaBar(usedPct float64, segments int) string {
	if segments <= 0 {
		segments = 20
	}
	rem := 100.0 - usedPct
	if rem < 0 {
		rem = 0
	}
	if rem > 100 {
		rem = 100
	}
	filled := int(math.Round((rem / 100.0) * float64(segments)))
	if filled < 0 {
		filled = 0
	}
	if filled > segments {
		filled = segments
	}
	empty := segments - filled

	fillColor := ColorGreen
	switch {
	case rem < 20:
		fillColor = ColorRed
	case rem <= 50:
		fillColor = ColorYellow
	}

	bracketSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	fillSt := lipgloss.NewStyle().Foreground(lipgloss.Color(fillColor))
	emptySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface1))

	return bracketSt.Render("[") +
		fillSt.Render(strings.Repeat("█", filled)) +
		emptySt.Render(strings.Repeat("░", empty)) +
		bracketSt.Render("]")
}

// ---------------------------------------------------------------------------
// Formatting helpers
// ---------------------------------------------------------------------------

func formatCompactTokens(n int64) string {
	if n < 0 {
		return "-" + formatCompactTokens(-n)
	}
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatUSD(v float64) string {
	if v < 0 {
		return "-" + formatUSD(-v)
	}
	if v > 0 && v < 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func formatPct(v float64, decimals int) string {
	if decimals <= 0 {
		return fmt.Sprintf("%.0f%%", v)
	}
	return fmt.Sprintf("%.*f%%", decimals, v)
}

func formatResetTime(unixSec int64, now time.Time) string {
	if unixSec <= 0 {
		return "—"
	}
	if now.IsZero() {
		now = time.Now()
	}
	t := time.Unix(unixSec, 0).In(now.Location())
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

func formatDurationShort(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	hrs := int(d.Hours()) % 24
	if hrs == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, hrs)
}

// ---------------------------------------------------------------------------
// Per-profile usage limit pace & formatting helpers
// ---------------------------------------------------------------------------

func parseLimitTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// computeLimitPace compares usedPercent against the elapsed fraction of the
// rate-limit window to classify pace as ahead, on, or under.
func computeLimitPace(w agent.UsageLimitWindow, now time.Time) (usage.LimitPace, bool) {
	if w.WindowDurationMins <= 0 || w.ResetsAt == "" {
		return "", false
	}
	resetAt, ok := parseLimitTime(w.ResetsAt)
	if !ok {
		return "", false
	}
	if now.IsZero() {
		now = time.Now()
	}
	total := time.Duration(w.WindowDurationMins) * time.Minute
	rem := resetAt.Sub(now)
	if rem <= 0 || rem >= total {
		return "", false
	}
	elapsedFrac := 1.0 - float64(rem)/float64(total)
	expectedUsed := elapsedFrac * 100.0
	delta := w.UsedPercent - expectedUsed
	switch {
	case delta > 8.0:
		return usage.LimitPaceAhead, true
	case delta < -8.0:
		return usage.LimitPaceUnder, true
	default:
		return usage.LimitPaceOn, true
	}
}

func renderLimitPaceTag(pace usage.LimitPace, usedPct float64) string {
	switch pace {
	case usage.LimitPaceAhead:
		color := ColorYellow
		if usedPct >= 80 {
			color = ColorRed
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render("▲ ahead")
	case usage.LimitPaceUnder:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen)).Render("▼ under")
	case usage.LimitPaceOn:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(ColorTeal)).Render("● on")
	default:
		return ""
	}
}

func formatLimitResetCountdown(resetsAt string, now time.Time, compact bool) string {
	t, ok := parseLimitTime(resetsAt)
	if !ok {
		return ""
	}
	if now.IsZero() {
		now = time.Now()
	}
	rem := t.Sub(now)
	if rem <= 0 {
		if compact {
			return "soon"
		}
		return "resets soon"
	}
	if rem < 24*time.Hour {
		h := int(rem.Hours())
		mins := int(rem.Minutes()) % 60
		var dur string
		switch {
		case h > 0 && mins > 0:
			if compact {
				dur = fmt.Sprintf("%dh%dm", h, mins)
			} else {
				dur = fmt.Sprintf("%dh %dm", h, mins)
			}
		case h > 0:
			dur = fmt.Sprintf("%dh", h)
		case mins > 0:
			dur = fmt.Sprintf("%dm", mins)
		default:
			dur = "<1m"
		}
		if compact {
			return "in " + dur
		}
		return "resets in " + dur
	}
	locT := t.In(now.Location())
	if compact {
		return locT.Format("Jan 2")
	}
	return "resets " + locT.Format("Jan 2 15:04")
}

func formatLimitExpiryDate(expiresAt string, now time.Time) string {
	t, ok := parseLimitTime(expiresAt)
	if !ok {
		return strings.TrimSpace(expiresAt)
	}
	if now.IsZero() {
		now = time.Now()
	}
	return t.In(now.Location()).Format("Jan 2")
}

func limitProviderDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "antigravity":
		return "Antigravity"
	case "opencode":
		return "OpenCode"
	case "cursor-agent", "cursor":
		return "Cursor"
	case "grok":
		return "Grok"
	case "muse":
		return "Muse"
	default:
		p := strings.TrimSpace(provider)
		if p == "" {
			return "Other"
		}
		return strings.ToUpper(p[:1]) + p[1:]
	}
}
