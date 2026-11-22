package screen

import (
	"regexp"
	"strings"
)

// ExtractTitle parses a captured pane viewport for agent-specific header,
// tab-bar, status-line, or prompt lines showing the conversation/session title.
func ExtractTitle(app, viewport string) string {
	if strings.TrimSpace(viewport) == "" {
		return ""
	}
	canonical := CanonicalApp(app)
	switch canonical {
	case "codex":
		if t := extractCodexTitle(viewport); t != "" {
			return t
		}
	case "muse":
		if t := extractMuseTitle(viewport); t != "" {
			return t
		}
	case "opencode", "kilo":
		if t := extractOpencodeTitle(viewport); t != "" {
			return t
		}
	case "agy":
		if t := extractAgyTitle(viewport); t != "" {
			return t
		}
	case "claude":
		if t := extractClaudeTitle(viewport); t != "" {
			return t
		}
	}
	return extractGenericTitle(viewport)
}

// CleanViewportTitle normalizes a candidate title extracted from a viewport,
// filtering out generic placeholders and bare slash commands.
func CleanViewportTitle(s string) string {
	return cleanViewportTitle(s)
}

// IsPathSegment reports whether s looks like a filesystem path segment.
func IsPathSegment(s string) bool {
	return isPathSegment(s)
}

// IsEffortSegment reports whether s is a reasoning-effort token in status footers.
func IsEffortSegment(s string) bool {
	return isEffortSegment(s)
}

// BottomNonEmptyLines returns the last n non-empty lines of viewport joined by "\n".
func BottomNonEmptyLines(viewport string, n int) string {
	return bottomNonEmptyLines(viewport, n)
}

// TopNonEmptyLines returns the first n non-empty lines of viewport joined by "\n".
func TopNonEmptyLines(viewport string, n int) string {
	return topNonEmptyLines(viewport, n)
}

// SplitLines splits s into individual lines (preserving leading whitespace).
func SplitLines(s string) []string {
	tls := splitTextLines(s)
	out := make([]string, len(tls))
	for i, tl := range tls {
		out[i] = tl.text
	}
	return out
}

// ExtractGenericTitle tries common prompt markers (❯, ›, >) to extract a title.
func ExtractGenericTitle(viewport string) string {
	return extractGenericTitle(viewport)
}

// ExtractPromptMarkerLine extracts the latest non-composer prompt line starting with marker.
func ExtractPromptMarkerLine(viewport, marker string) string {
	return extractPromptMarkerLine(viewport, marker)
}

// ExtractMultilinePrompt extracts a prompt starting with marker and up to 3 indented continuation lines.
func ExtractMultilinePrompt(viewport, marker string) string {
	return extractMultilinePrompt(viewport, marker)
}

func cleanViewportTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	lower := strings.ToLower(s)
	switch lower {
	case "", "new session", "untitled", "launch overrides", "ask codex to do anything":
		return ""
	}
	if strings.HasPrefix(lower, "new session - ") {
		return ""
	}
	if strings.HasPrefix(s, "/") {
		fields := strings.Fields(s)
		if len(fields) <= 1 {
			return ""
		}
		switch strings.ToLower(fields[0]) {
		case "/model", "/login", "/logout", "/clear", "/compact", "/help", "/status", "/config", "/setup":
			return ""
		default:
			s = strings.Join(fields[1:], " ")
		}
	}
	r := []rune(s)
	if len(r) > 160 {
		return string(r[:159]) + "…"
	}
	return s
}

func isPathSegment(s string) bool {
	s = strings.TrimSpace(s)
	return s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./")
}

func isEffortSegment(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "ultra", "auto", "plan", "default":
		return true
	}
	return false
}

// extractCodexTitle parses Codex footer lines (`<model> <effort> · <cwd> · <Title>`)
// or the latest user prompt line (`› <prompt>`).
func extractCodexTitle(viewport string) string {
	bottom := bottomNonEmptyLines(viewport, 6)
	for _, tl := range splitTextLines(bottom) {
		line := strings.TrimSpace(tl.text)
		if !strings.Contains(line, " · ") {
			continue
		}
		if strings.Contains(line, "limit:") || strings.Contains(line, "resets at") || strings.Contains(line, "warnings") {
			continue
		}
		parts := strings.Split(line, " · ")
		if len(parts) >= 3 && isPathSegment(parts[1]) {
			if t := cleanViewportTitle(strings.Join(parts[2:], " · ")); t != "" {
				return t
			}
		}
	}
	if t := extractPromptMarkerLine(viewport, "›"); t != "" {
		return t
	}
	return ""
}

var museTokenMeterRe = regexp.MustCompile(`\s+\d+(?:\.\d+)?[kKmM]?/\d+(?:\.\d+)?[kKmM]?(?:\s*\(\d+(?:\.\d+)?%\))?\s*$`)

// extractMuseTitle parses Muse footer lines (`<model> · <effort> · <cwd> · <Title> [tokens]`)
// or user prompt lines (`❯ <prompt>`).
func extractMuseTitle(viewport string) string {
	bottom := bottomNonEmptyLines(viewport, 5)
	for _, tl := range splitTextLines(bottom) {
		line := strings.TrimSpace(tl.text)
		parts := strings.Split(line, " · ")
		if len(parts) >= 4 && isEffortSegment(parts[1]) && isPathSegment(parts[2]) {
			rawTitle := museTokenMeterRe.ReplaceAllString(strings.Join(parts[3:], " · "), "")
			if t := cleanViewportTitle(rawTitle); t != "" {
				return t
			}
		}
	}
	if t := extractPromptMarkerLine(viewport, "❯"); t != "" {
		return t
	}
	return ""
}

var opencodeTabLeadRe = regexp.MustCompile(`^[‹<]\d+\s+`)
var multiSpaceRe = regexp.MustCompile(`\s{2,}`)

// extractOpencodeTitle parses OpenCode/Kilo top tab bars (`‹3  Tab1  ActiveTitle + ...`)
// or boxed user prompts (`┃  <prompt>`).
func extractOpencodeTitle(viewport string) string {
	top := topNonEmptyLines(viewport, 4)
	for _, tl := range splitTextLines(top) {
		line := strings.TrimSpace(tl.text)
		if opencodeTabLeadRe.MatchString(line) && strings.Contains(line, "+") {
			rest := opencodeTabLeadRe.ReplaceAllString(line, "")
			segs := multiSpaceRe.Split(rest, -1)
			plusIdx := -1
			for i, seg := range segs {
				s := strings.TrimSpace(seg)
				if s == "+" || strings.HasSuffix(s, " +") {
					plusIdx = i
					break
				}
			}
			if plusIdx >= 0 {
				if s := strings.TrimSpace(segs[plusIdx]); strings.HasSuffix(s, " +") {
					if t := cleanViewportTitle(strings.TrimSuffix(s, " +")); t != "" {
						return t
					}
				}
				for i := plusIdx - 1; i >= 0; i-- {
					if t := cleanViewportTitle(segs[i]); t != "" {
						return t
					}
				}
			}
		}
	}
	// Fallback: boxed user prompt block (`┃  <prompt>`)
	lines := splitTextLines(viewport)
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i].text)
		if strings.HasPrefix(trimmed, "┃") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "┃"))
			if rest == "" || strings.Contains(rest, " · ") {
				continue
			}
			if t := cleanViewportTitle(rest); t != "" {
				return t
			}
		}
	}
	return ""
}

// extractAgyTitle parses Antigravity CLI viewports for the conversation prompt
// following a horizontal separator (`───\n> <prompt>`).
func extractAgyTitle(viewport string) string {
	return extractMultilinePrompt(viewport, ">")
}

// extractClaudeTitle parses Claude Code viewports for the conversation prompt
// (`❯ <prompt>`).
func extractClaudeTitle(viewport string) string {
	return extractPromptMarkerLine(viewport, "❯")
}

func extractGenericTitle(viewport string) string {
	for _, marker := range []string{"❯", "›", ">"} {
		if t := extractPromptMarkerLine(viewport, marker); t != "" {
			return t
		}
	}
	return ""
}

func extractPromptMarkerLine(viewport, marker string) string {
	lines := splitTextLines(viewport)
	var found string
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i].text)
		if !strings.HasPrefix(line, marker) {
			continue
		}
		// Ignore composer box prompt lines (immediately preceded and followed by horizontal rules).
		if i > 0 && i+1 < len(lines) && isHorizontalRule(lines[i-1].text) && isHorizontalRule(lines[i+1].text) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, marker))
		if rest == "" {
			continue
		}
		if strings.HasPrefix(rest, "Auto mode:") || strings.HasPrefix(rest, "You are in ") {
			continue
		}
		if t := cleanViewportTitle(rest); t != "" {
			found = t
		}
	}
	return found
}

func extractMultilinePrompt(viewport, marker string) string {
	lines := splitTextLines(viewport)
	var found string
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i].text)
		if !strings.HasPrefix(line, marker) {
			continue
		}
		if i > 0 && i+1 < len(lines) && isHorizontalRule(lines[i-1].text) && isHorizontalRule(lines[i+1].text) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, marker))
		if rest == "" || strings.HasPrefix(rest, "Auto mode:") || strings.Contains(rest, " Default ") {
			continue
		}
		parts := []string{rest}
		for j := i + 1; j < len(lines) && j <= i+3; j++ {
			nextRaw := lines[j].text
			nextTrim := strings.TrimSpace(nextRaw)
			if nextTrim == "" || isHorizontalRule(nextTrim) ||
				strings.HasPrefix(nextTrim, "●") || strings.HasPrefix(nextTrim, "▸") ||
				strings.HasPrefix(nextTrim, "◆") || strings.HasPrefix(nextTrim, "•") ||
				strings.HasPrefix(nextTrim, "⎿") || strings.HasPrefix(nextTrim, marker) {
				break
			}
			if strings.HasPrefix(nextRaw, "  ") {
				parts = append(parts, nextTrim)
			} else {
				break
			}
		}
		if t := cleanViewportTitle(strings.Join(parts, " ")); t != "" {
			found = t
		}
	}
	return found
}
