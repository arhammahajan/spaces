package model

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	fuzzyBaseMatch        = 16
	fuzzyPrefixBonus      = 24
	fuzzyWordBoundary     = 14
	fuzzyCamelBoundary    = 12
	fuzzyConsecutiveBonus = 18
	fuzzyExactCaseBonus   = 1
	fuzzyGapStartPenalty  = 3
	fuzzyGapExtendPenalty = 1
	fuzzyMaxLeadPenalty   = 12
	fuzzyDPMaxPat         = 64
	fuzzyDPMaxText        = 512
)

// FuzzyMatch performs a Unicode-safe, case-insensitive fzf-style subsequence
// match of pattern against text. It returns a score (higher is better), the
// sorted matched rune indices in text, and whether pattern matched.
func FuzzyMatch(pattern, text string) (int, []int, bool) {
	if pattern == "" {
		return 0, nil, true
	}
	pat := []rune(pattern)
	txt := []rune(text)
	m := len(pat)
	n := len(txt)
	if m == 0 {
		return 0, nil, true
	}
	if m > n {
		return 0, nil, false
	}

	lowPat := make([]rune, m)
	for i, r := range pat {
		lowPat[i] = unicode.ToLower(r)
	}
	lowTxt := make([]rune, n)
	for i, r := range txt {
		lowTxt[i] = unicode.ToLower(r)
	}

	// Fast O(N) feasibility check & initial greedy positions.
	firstEnd := -1
	pi := 0
	for ti := 0; ti < n && pi < m; ti++ {
		if lowTxt[ti] == lowPat[pi] {
			pi++
			if pi == m {
				firstEnd = ti
			}
		}
	}
	if pi < m {
		return 0, nil, false
	}

	// Exact DP alignment for typical field sizes; backward-tightened fallback
	// for unusually long strings so latency stays bounded.
	if m <= fuzzyDPMaxPat && n <= fuzzyDPMaxText {
		return fuzzyMatchDP(pat, lowPat, txt, lowTxt)
	}
	return fuzzyMatchTight(pat, lowPat, txt, lowTxt, firstEnd)
}

func isWordSeparator(r rune) bool {
	switch r {
	case '/', '\\', '-', '_', '.', ':', ' ', '\t', '(', ')', '[', ']', '{', '}', ',', ';':
		return true
	default:
		return false
	}
}

func boundaryBonusAt(txt []rune, idx int) int {
	if idx == 0 {
		return fuzzyPrefixBonus
	}
	prev := txt[idx-1]
	cur := txt[idx]
	if isWordSeparator(prev) && !isWordSeparator(cur) {
		return fuzzyWordBoundary
	}
	if unicode.IsLower(prev) && unicode.IsUpper(cur) {
		return fuzzyCamelBoundary
	}
	return 0
}

func fuzzyMatchDP(pat, lowPat, txt, lowTxt []rune) (int, []int, bool) {
	m := len(pat)
	n := len(txt)
	const negInf = math.MinInt32 / 4

	bonuses := make([]int, n)
	for j := 0; j < n; j++ {
		bonuses[j] = boundaryBonusAt(txt, j)
	}

	prevRow := make([]int, n)
	currRow := make([]int, n)
	parent := make([]int, m*n)
	for i := range parent {
		parent[i] = -1
	}

	// Row 0: match pat[0].
	for j := 0; j < n; j++ {
		if lowTxt[j] == lowPat[0] {
			leadPen := j
			if leadPen > fuzzyMaxLeadPenalty {
				leadPen = fuzzyMaxLeadPenalty
			}
			s := fuzzyBaseMatch + bonuses[j] - leadPen
			if txt[j] == pat[0] {
				s += fuzzyExactCaseBonus
			}
			prevRow[j] = s
		} else {
			prevRow[j] = negInf
		}
	}

	// Subsequent rows with O(N) affine gap transition.
	for i := 1; i < m; i++ {
		for j := 0; j < n; j++ {
			currRow[j] = negInf
		}
		bestGap := negInf
		bestGapIdx := -1
		for j := i; j < n; j++ {
			if bestGapIdx >= 0 {
				bestGap -= fuzzyGapExtendPenalty
			}
			if j >= 2 && prevRow[j-2] > negInf {
				cand := prevRow[j-2] - fuzzyGapStartPenalty
				if cand > bestGap {
					bestGap = cand
					bestGapIdx = j - 2
				}
			}
			if lowTxt[j] != lowPat[i] {
				continue
			}
			bestScore := negInf
			bestPrev := -1

			if prevRow[j-1] > negInf {
				s := prevRow[j-1] + fuzzyBaseMatch + fuzzyConsecutiveBonus + bonuses[j]/2
				if txt[j] == pat[i] {
					s += fuzzyExactCaseBonus
				}
				if s > bestScore {
					bestScore = s
					bestPrev = j - 1
				}
			}
			if bestGapIdx >= 0 && bestGap > negInf {
				s := bestGap + fuzzyBaseMatch + bonuses[j]
				if txt[j] == pat[i] {
					s += fuzzyExactCaseBonus
				}
				if s > bestScore {
					bestScore = s
					bestPrev = bestGapIdx
				}
			}
			currRow[j] = bestScore
			parent[i*n+j] = bestPrev
		}
		prevRow, currRow = currRow, prevRow
	}

	bestEnd := -1
	bestScore := negInf
	for j := m - 1; j < n; j++ {
		if prevRow[j] > bestScore {
			bestScore = prevRow[j]
			bestEnd = j
		}
	}
	if bestEnd < 0 || bestScore <= negInf {
		return 0, nil, false
	}

	positions := make([]int, m)
	idx := bestEnd
	for i := m - 1; i >= 0; i-- {
		positions[i] = idx
		idx = parent[i*n+idx]
	}

	// Gentle length/trailing preference so exact and compact strings win ties.
	trailing := n - 1 - bestEnd
	if trailing > 0 {
		pen := trailing / 4
		if pen > 10 {
			pen = 10
		}
		bestScore -= pen
	}
	if m == n && bestEnd == n-1 {
		bestScore += 12
	}
	return bestScore, positions, true
}

func fuzzyMatchTight(pat, lowPat, txt, lowTxt []rune, end int) (int, []int, bool) {
	m := len(pat)
	n := len(txt)
	positions := make([]int, m)
	pi := m - 1
	for ti := end; ti >= 0 && pi >= 0; ti-- {
		if lowTxt[ti] == lowPat[pi] {
			positions[pi] = ti
			pi--
		}
	}
	if pi >= 0 {
		return 0, nil, false
	}
	score := 0
	for i, pos := range positions {
		b := boundaryBonusAt(txt, pos)
		if i == 0 {
			leadPen := pos
			if leadPen > fuzzyMaxLeadPenalty {
				leadPen = fuzzyMaxLeadPenalty
			}
			score += fuzzyBaseMatch + b - leadPen
		} else {
			gap := pos - positions[i-1] - 1
			if gap == 0 {
				score += fuzzyBaseMatch + fuzzyConsecutiveBonus + b/2
			} else {
				score += fuzzyBaseMatch + b - fuzzyGapStartPenalty - (gap-1)*fuzzyGapExtendPenalty
			}
		}
		if txt[pos] == pat[i] {
			score += fuzzyExactCaseBonus
		}
	}
	trailing := n - 1 - positions[m-1]
	if trailing > 0 {
		pen := trailing / 4
		if pen > 10 {
			pen = 10
		}
		score -= pen
	}
	return score, positions, true
}

// ParseConversationQuery extracts inline `@<agent>` filter tokens (supporting
// aliases like `@agy` -> `antigravity`, `@claude-code` -> `claude`, `@dsh` ->
// `deepseek`) and returns the canonical agent filter plus remaining whitespace
// search tokens.
func ParseConversationQuery(raw string) (agentFilter string, tokens []string) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return "", nil
	}
	tokens = make([]string, 0, len(fields))
	for _, f := range fields {
		if strings.HasPrefix(f, "@") && len(f) > 1 {
			if canon := CanonicalConversationAgent(f[1:]); canon != "" {
				agentFilter = canon
				continue
			}
		}
		tokens = append(tokens, f)
	}
	return agentFilter, tokens
}

// FuzzyScoreConversation evaluates multi-token AND fuzzy matching across a
// conversation's searchable metadata fields with field weights:
// Title > Harness/Profile > Project > Branch > Preview > CWD/Space/Model/ID.
//
// It returns the aggregate score (higher is better, including a mild recency
// tie-break), sorted matched rune indices on conv.Title, and whether all tokens
// matched.
func FuzzyScoreConversation(conv ConversationEntry, tokens []string, nowMs int64) (int, []int, bool) {
	recency := recencyTieBreak(conv.EffectiveTimeMs(), nowMs)
	if len(tokens) == 0 {
		return recency, nil, true
	}

	displayApp := conv.DisplayApp()
	agent := strings.TrimSpace(conv.Agent)
	profile := strings.TrimSpace(conv.Profile)
	harnessCombined := strings.TrimSpace(displayApp + " " + profile)
	project := conv.ProjectName()

	type weightedField struct {
		text    string
		mult    int
		base    int
		isTitle bool
	}
	fields := []weightedField{
		{text: conv.Title, mult: 4, base: 240, isTitle: true},
		{text: displayApp, mult: 3, base: 180},
		{text: agent, mult: 3, base: 180},
		{text: profile, mult: 3, base: 175},
		{text: harnessCombined, mult: 3, base: 170},
		{text: project, mult: 3, base: 145},
		{text: conv.Branch, mult: 2, base: 120},
		{text: conv.Preview, mult: 2, base: 85},
		{text: conv.CWD, mult: 1, base: 55},
		{text: conv.Space, mult: 1, base: 55},
		{text: conv.Model, mult: 1, base: 50},
		{text: conv.ID, mult: 1, base: 45},
	}

	totalScore := 0
	var titlePosSet map[int]struct{}

	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		bestTokScore := math.MinInt32
		matched := false
		for _, f := range fields {
			if f.text == "" {
				continue
			}
			s, pos, ok := FuzzyMatch(tok, f.text)
			if !ok {
				continue
			}
			matched = true
			if f.isTitle && len(pos) > 0 {
				if titlePosSet == nil {
					titlePosSet = make(map[int]struct{}, len(pos))
				}
				for _, idx := range pos {
					titlePosSet[idx] = struct{}{}
				}
			}
			weighted := f.base + s*f.mult
			if weighted > bestTokScore {
				bestTokScore = weighted
			}
		}
		if !matched {
			return 0, nil, false
		}
		totalScore += bestTokScore
	}

	var highlights []int
	if len(titlePosSet) > 0 {
		highlights = make([]int, 0, len(titlePosSet))
		for idx := range titlePosSet {
			highlights = append(highlights, idx)
		}
		sort.Ints(highlights)
	}
	return totalScore + recency, highlights, true
}

// recencyTieBreak returns a mild 0..24 recency bonus so equally-scored matches
// prefer newer conversations without overriding stronger text matches.
func recencyTieBreak(tsMs, nowMs int64) int {
	if tsMs <= 0 || nowMs <= 0 {
		return 0
	}
	ageMs := nowMs - tsMs
	if ageMs <= 0 {
		return 24
	}
	const (
		hourMs = int64(3600 * 1000)
		dayMs  = 24 * hourMs
	)
	switch {
	case ageMs < hourMs:
		return 24
	case ageMs < 6*hourMs:
		return 20
	case ageMs < dayMs:
		return 16
	case ageMs < 7*dayMs:
		return 12
	case ageMs < 30*dayMs:
		return 6
	default:
		return 2
	}
}
