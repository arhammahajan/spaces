package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/arhammahajan/spaces/internal/agent"
)

// brailleUp maps (prev_level * 5 + cur_level) for 0..4 vertical bar heights
// in left/right braille columns, matching abtop / btop_draw.cpp.
var brailleUp = [25]string{
	" ", "⢀", "⢠", "⢰", "⢸",
	"⡀", "⣀", "⣠", "⣰", "⣸",
	"⡄", "⣄", "⣤", "⣴", "⣼",
	"⡆", "⣆", "⣦", "⣶", "⣾",
	"⡇", "⣇", "⣧", "⣷", "⣿",
}

func clampFloat(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parseHexRGB(hex string) (int, int, int) {
	s := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(s) != 6 {
		return 205, 214, 244 // ColorText fallback
	}
	r, errR := strconv.ParseInt(s[0:2], 16, 32)
	g, errG := strconv.ParseInt(s[2:4], 16, 32)
	b, errB := strconv.ParseInt(s[4:6], 16, 32)
	if errR != nil || errG != nil || errB != nil {
		return 205, 214, 244
	}
	return int(r), int(g), int(b)
}

// lerpHexColor linearly interpolates along a 3-stop RGB gradient (0..50: start→mid, 50..100: mid→end).
func lerpHexColor(pct float64, startHex, midHex, endHex string) string {
	p := clampFloat(pct, 0, 100)
	var r1, g1, b1, r2, g2, b2 int
	var t float64
	if p <= 50 {
		r1, g1, b1 = parseHexRGB(startHex)
		r2, g2, b2 = parseHexRGB(midHex)
		t = p / 50.0
	} else {
		r1, g1, b1 = parseHexRGB(midHex)
		r2, g2, b2 = parseHexRGB(endHex)
		t = (p - 50.0) / 50.0
	}
	clampByte := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return v
	}
	r := clampByte(int(math.Round(float64(r1) + t*float64(r2-r1))))
	g := clampByte(int(math.Round(float64(g1) + t*float64(g2-g1))))
	b := clampByte(int(math.Round(float64(b1) + t*float64(b2-b1))))
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// meterBar renders an abtop-style ■ meter bar where filled cells step progressively
// along the RGB gradient startHex→midHex→endHex and unfilled cells use ColorSurface1.
func meterBar(pct float64, width int, startHex, midHex, endHex string) string {
	if width <= 0 {
		return ""
	}
	clamped := clampFloat(pct, 0, 100)
	filled := int(math.Round((clamped / 100.0) * float64(width)))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	emptySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface1))
	var b strings.Builder
	for i := 0; i < width; i++ {
		if i < filled {
			cellPct := (float64(i) / float64(width)) * 100.0
			hex := lerpHexColor(cellPct, startHex, midHex, endHex)
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render("■"))
		} else {
			b.WriteString(emptySt.Render("■"))
		}
	}
	return b.String()
}

// remainingBar renders a quota remaining bar: fills proportionally to (100 - usedPct),
// colored uniformly by usedPct (ColorGreen → ColorYellow → ColorRed).
func remainingBar(usedPct float64, width int) string {
	if width <= 0 {
		return ""
	}
	used := clampFloat(usedPct, 0, 100)
	remaining := 100.0 - used
	filled := int(math.Round((remaining / 100.0) * float64(width)))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	fillHex := lerpHexColor(used, ColorGreen, ColorYellow, ColorRed)
	fillSt := lipgloss.NewStyle().Foreground(lipgloss.Color(fillHex))
	emptySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface1))

	var b strings.Builder
	for i := 0; i < width; i++ {
		if i < filled {
			b.WriteString(fillSt.Render("■"))
		} else {
			b.WriteString(emptySt.Render("■"))
		}
	}
	return b.String()
}

func normalizeSeries(values []float64, needed int) []float64 {
	sampled := make([]float64, needed)
	if len(values) == 0 {
		return sampled
	}
	maxVal := 0.0
	for _, v := range values {
		if v > maxVal {
			maxVal = v
		}
	}
	scale := 1.0
	if maxVal > 1.0 {
		scale = maxVal
	}
	if len(values) >= needed {
		tail := values[len(values)-needed:]
		for i, v := range tail {
			sampled[i] = clampFloat(v/scale, 0, 1)
		}
	} else {
		offset := needed - len(values)
		for i, v := range values {
			sampled[offset+i] = clampFloat(v/scale, 0, 1)
		}
	}
	return sampled
}

// brailleSparkline renders a 1-row Unicode braille sparkline (2 samples per cell)
// colored along the startHex→midHex→endHex gradient.
func brailleSparkline(values []float64, width int, startHex, midHex, endHex string) string {
	if width <= 0 {
		return ""
	}
	emptySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	if len(values) == 0 {
		return emptySt.Render(strings.Repeat(" ", width))
	}
	sampled := normalizeSeries(values, width*2)
	var b strings.Builder
	for i := 0; i < width; i++ {
		v0 := sampled[i*2]
		v1 := sampled[i*2+1]
		prev := int(math.Round(v0 * 4.0))
		cur := int(math.Round(v1 * 4.0))
		idx := prev*5 + cur
		if idx < 0 {
			idx = 0
		}
		if idx > 24 {
			idx = 24
		}
		if idx == 0 {
			b.WriteString(emptySt.Render(" "))
			continue
		}
		peak := v1
		if v0 > peak {
			peak = v0
		}
		hex := lerpHexColor(peak*100.0, startHex, midHex, endHex)
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render(brailleUp[idx]))
	}
	return b.String()
}

// brailleGraphMultirow renders a multi-row braille area graph (4 vertical dots × 2 samples
// per cell), returning one styled string per terminal row from top to bottom.
func brailleGraphMultirow(values []float64, width, height int, startHex, midHex, endHex string) []string {
	if height <= 0 {
		return nil
	}
	rows := make([]string, height)
	if width <= 0 {
		return rows
	}

	totalVRes := height * 4
	sampled := normalizeSeries(values, width*2)
	heights := make([]int, len(sampled))
	for i, v := range sampled {
		heights[i] = int(math.Round(v * float64(totalVRes)))
	}

	// Braille dot bits bottom-to-top within each cell:
	// Left col:  row0(bottom)=0x40, row1=0x04, row2=0x02, row3(top)=0x01
	// Right col: row0(bottom)=0x80, row1=0x20, row2=0x10, row3(top)=0x08
	leftBits := [4]rune{0x40, 0x04, 0x02, 0x01}
	rightBits := [4]rune{0x80, 0x20, 0x10, 0x08}
	emptySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))

	for row := 0; row < height; row++ {
		invRow := height - 1 - row
		baseY := invRow * 4
		var b strings.Builder
		for col := 0; col < width; col++ {
			leftH := heights[col*2]
			rightH := heights[col*2+1]
			var pattern rune
			for dotRow := 0; dotRow < 4; dotRow++ {
				yPos := baseY + dotRow
				if leftH > yPos {
					pattern |= leftBits[dotRow]
				}
				if rightH > yPos {
					pattern |= rightBits[dotRow]
				}
			}
			ch := string(rune(0x2800) + pattern)
			if pattern == 0 {
				b.WriteString(emptySt.Render(ch))
				continue
			}
			maxVal := sampled[col*2]
			if sampled[col*2+1] > maxVal {
				maxVal = sampled[col*2+1]
			}
			hex := lerpHexColor(maxVal*100.0, startHex, midHex, endHex)
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render(ch))
		}
		rows[row] = b.String()
	}
	return rows
}

// fmtTokens formats a token count compactly (e.g. 1.2M, 42.5k, 200k, 850).
func fmtTokens(n int64) string {
	if n < 0 {
		n = 0
	}
	if n >= 1_000_000 {
		v := float64(n) / 1_000_000.0
		if v == math.Trunc(v) {
			return fmt.Sprintf("%dM", int64(v))
		}
		return fmt.Sprintf("%.1fM", v)
	}
	if n >= 1_000 {
		v := float64(n) / 1_000.0
		if v == math.Trunc(v) {
			return fmt.Sprintf("%dk", int64(v))
		}
		return fmt.Sprintf("%.1fk", v)
	}
	return fmt.Sprintf("%d", n)
}

// fmtMemKB formats process memory in kilobytes (e.g. 128M, 1.4G, —).
func fmtMemKB(kb int64) string {
	if kb <= 0 {
		return "—"
	}
	if kb >= 1_048_576 {
		return fmt.Sprintf("%.1fG", float64(kb)/1_048_576.0)
	}
	if kb >= 1024 {
		return fmt.Sprintf("%dM", (kb+512)/1024)
	}
	return fmt.Sprintf("%dK", kb)
}

// fmtCountdown formats a future reset timestamp as "in X" (e.g. "in 2h 14m", "in 3d 4h").
// Returns empty string when resetUnix is in the past or zero.
func fmtCountdown(resetUnix, nowUnix int64) string {
	if resetUnix <= nowUnix {
		return ""
	}
	diff := resetUnix - nowUnix
	switch {
	case diff < 60:
		return fmt.Sprintf("in %ds", diff)
	case diff < 3600:
		return fmt.Sprintf("in %dm", diff/60)
	case diff < 86400:
		return fmt.Sprintf("in %dh %dm", diff/3600, (diff%3600)/60)
	default:
		return fmt.Sprintf("in %dd %dh", diff/86400, (diff%86400)/3600)
	}
}

// fmtRelativeAge formats a past unix timestamp as a compact age ("12s ago", "4m ago", "2h ago", "3d ago").
func fmtRelativeAge(unixSec, nowUnix int64) string {
	if unixSec <= 0 {
		return "—"
	}
	diff := nowUnix - unixSec
	if diff < 0 {
		diff = 0
	}
	switch {
	case diff < 60:
		return fmt.Sprintf("%ds ago", diff)
	case diff < 3600:
		return fmt.Sprintf("%dm ago", diff/60)
	case diff < 86400:
		return fmt.Sprintf("%dh ago", diff/3600)
	default:
		return fmt.Sprintf("%dd ago", diff/86400)
	}
}

// shortenModel compacts model identifiers (e.g. "claude-opus-4-6" -> "opus4.6"),
// appending "[1m]" when ctxWindow >= 1_000_000 or the model already tags "[1m]".
func shortenModel(modelName string, ctxWindow int64) string {
	s := strings.TrimSpace(modelName)
	if s == "" {
		return "-"
	}
	lower := strings.ToLower(s)
	is1M := ctxWindow >= 1_000_000 || strings.Contains(lower, "[1m]")
	if strings.HasPrefix(lower, "claude-") {
		s = s[len("claude-"):]
	}
	if len(s) >= 4 && strings.EqualFold(s[len(s)-4:], "[1m]") {
		s = s[:len(s)-4]
	}
	digitPos := -1
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			digitPos = i
			break
		}
	}
	base := s
	if digitPos >= 0 {
		name := strings.TrimRight(s[:digitPos], "-")
		ver := strings.ReplaceAll(s[digitPos:], "-", ".")
		base = name + ver
	}
	if is1M {
		return base + "[1m]"
	}
	return base
}

// agentBadge returns the compact abtop-style agent glyph badge plus optional
// launch surface suffix ("a" for desktop app, "i" for IDE extension).
func agentBadge(agentID string, surface agent.LaunchSurface) string {
	base := ""
	switch strings.ToLower(strings.TrimSpace(agentID)) {
	case "claude", "claude-code":
		base = "*CC"
	case "codex":
		base = ">CD"
	case "opencode":
		base = "#OC"
	case "gemini", "gemini-cli":
		base = "✦GM"
	case "cursor", "cursor-agent":
		base = "▸CR"
	default:
		clean := strings.ToUpper(strings.TrimSpace(agentID))
		if len(clean) > 3 {
			clean = clean[:3]
		}
		if clean == "" {
			clean = "???"
		}
		base = clean
	}
	return base + surface.ShortSuffix()
}

func agentBadgeColor(agentID string) string {
	switch strings.ToLower(strings.TrimSpace(agentID)) {
	case "claude", "claude-code":
		return "#d97757"
	case "codex":
		return ColorBlue
	case "opencode":
		return ColorGreen
	case "gemini", "gemini-cli":
		return ColorMauve
	case "cursor", "cursor-agent":
		return ColorTeal
	default:
		return ColorOverlay0
	}
}
