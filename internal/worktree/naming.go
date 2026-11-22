package worktree

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	maxSlugLength    = 36
	maxDirNameLength = 64
	maxSlugWords     = 5
)

// 64 curated, readable lowercase adjectives for memorable worktree names.
var namingAdjectives = [64]string{
	"amber", "avid", "bold", "bright", "brisk", "calm", "clear", "cobalt",
	"cool", "coral", "crisp", "daring", "deft", "eager", "fair", "firm",
	"fleet", "fresh", "gentle", "glad", "golden", "grand", "green", "hale",
	"happy", "hardy", "ivory", "jade", "keen", "kind", "light", "lively",
	"lucid", "lunar", "mellow", "mint", "misty", "neat", "noble", "nordic",
	"ochre", "olive", "opal", "plain", "plucky", "prime", "pure", "quick",
	"quiet", "rapid", "ready", "rosy", "royal", "ruby", "sage", "sharp",
	"silver", "sleek", "smart", "smooth", "solar", "solid", "steady", "swift",
}

// 64 curated, readable lowercase nouns for memorable worktree names.
var namingNouns = [64]string{
	"acorn", "alder", "anchor", "anvil", "arch", "aspen", "atlas", "badger",
	"beacon", "birch", "bison", "brook", "cairn", "canyon", "cedar", "cliff",
	"clover", "comet", "condor", "crane", "creek", "crest", "delta", "drift",
	"eagle", "ember", "falcon", "fern", "finch", "fjord", "flint", "forge",
	"fox", "glade", "grove", "harbor", "haven", "hawk", "heron", "island",
	"jasper", "kestrel", "larch", "lark", "laurel", "ledge", "linden", "lynx",
	"maple", "meadow", "mesa", "otter", "owl", "peak", "pebble", "pine",
	"plover", "quartz", "raven", "reef", "ridge", "river", "robin", "spruce",
}

var genericShellNames = map[string]bool{
	"zsh":      true,
	"bash":     true,
	"sh":       true,
	"fish":     true,
	"nu":       true,
	"nushell":  true,
	"tcsh":     true,
	"csh":      true,
	"ksh":      true,
	"dash":     true,
	"shell":    true,
	"terminal": true,
	"term":     true,
	"tmux":     true,
	"login":    true,
	"console":  true,
}

var genericTargetNames = map[string]bool{
	"space":    true,
	"tab":      true,
	"window":   true,
	"pane":     true,
	"default":  true,
	"untitled": true,
	"new":      true,
}

var knownCodingAgents = map[string]string{
	"claude":          "claude",
	"claude-code":     "claude",
	"claude_code":     "claude",
	"claudecode":      "claude",
	"antigravity":     "agy",
	"antigravity-cli": "agy",
	"agy":             "agy",
	"cursor":          "cursor",
	"cursor-agent":    "cursor",
	"cursor_agent":    "cursor",
	"cursor-cli":      "cursor",
	"codex":           "codex",
	"openai-codex":    "codex",
	"opencode":        "opencode",
	"open-code":       "opencode",
	"oc":              "opencode",
	"gemini":          "gemini",
	"gemini-cli":      "gemini",
	"aider":           "aider",
	"amp":             "amp",
	"copilot":         "copilot",
	"github-copilot":  "copilot",
	"goose":           "goose",
	"pi":              "pi",
	"windsurf":        "windsurf",
	"cline":           "cline",
	"roo":             "roo",
	"roo-code":        "roo",
	"qwen":            "qwen",
	"qwen-code":       "qwen",
	"kiro":            "kiro",
	"droid":           "droid",
	"crush":           "crush",
	"kimi":            "kimi",
}

var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
	"of": true, "in": true, "on": true, "at": true, "to": true, "for": true,
	"from": true, "by": true, "with": true, "into": true, "onto": true,
	"about": true, "as": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "it": true, "its": true, "this": true,
	"that": true, "these": true, "those": true, "i": true, "me": true, "my": true,
	"we": true, "our": true, "you": true, "your": true, "please": true, "can": true,
	"could": true, "would": true, "should": true, "will": true, "just": true,
	"want": true, "need": true, "help": true, "let": true, "lets": true, "make": true,
	"do": true, "does": true, "did": true, "have": true, "has": true, "had": true,
	"all": true, "some": true, "any": true, "so": true, "if": true, "then": true,
	"than": true, "there": true, "here": true, "when": true, "where": true,
	"how": true, "what": true, "which": true, "who": true, "why": true,
	"hey": true, "hi": true, "hello": true, "thanks": true, "thank": true,
}

var (
	urlRe           = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	mdLinkRe        = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	versionBannerRe = regexp.MustCompile(`(?i)^(?:[a-z0-9._-]+\s+)?v?\d+\.\d+(?:\.\d+)?(?:[-+][a-z0-9._-]+)?$`)
	numberedGenRe   = regexp.MustCompile(`(?i)^(?:tab|space|window|pane|zsh|bash|sh|fish|shell|terminal)[-_\s#]*\d+$`)
)

// SuggestName produces a worktree branch and directory name from NamingContext
// following the 6-tier priority ruleset while avoiding collisions in taken.
func SuggestName(ctx NamingContext, taken map[string]bool) NamingResult {
	// Tier 1: Explicit user-provided name. Never auto-suffixed or prefixed.
	if explicit := strings.TrimSpace(ctx.ExplicitName); explicit != "" {
		branch := SanitizeBranchName(explicit)
		if branch != "" {
			return NamingResult{
				Branch:  branch,
				DirName: WorktreeDirName(branch),
				Source:  "custom",
				Rule:    "explicit",
			}
		}
	}

	prefix := resolvePrefix(ctx)
	curNorm := normalizeCompare(ctx.CurrentBranch)
	repoNorm := normalizeCompare(ctx.RepoName)

	// Tier 2: Meaningful target (tab/space) name (skipped on Seed > 0 regeneration).
	if ctx.Seed == 0 {
		if slug := extractTargetSlug(ctx.TargetName, repoNorm, curNorm, ctx.HarnessApp); slug != "" {
			branch := disambiguate(applyPrefix(prefix, slug), taken)
			return NamingResult{
				Branch:  branch,
				DirName: WorktreeDirName(branch),
				Source:  targetSourceLabel(ctx.TargetKind),
				Rule:    "target_name",
			}
		}
	}

	// Tier 3: Harness title (only when Seed == 0 so regeneration cycles to random names).
	if ctx.Seed == 0 {
		if slug := extractHarnessTitleSlug(ctx.HarnessTitle, repoNorm, curNorm); slug != "" {
			branch := disambiguate(applyPrefix(prefix, slug), taken)
			return NamingResult{
				Branch:  branch,
				DirName: WorktreeDirName(branch),
				Source:  harnessSourceLabel(ctx.HarnessApp, "title"),
				Rule:    "harness_title",
			}
		}
	}

	// Tier 4: Harness prompt (only when Seed == 0).
	if ctx.Seed == 0 {
		if slug := extractHarnessPromptSlug(ctx.HarnessPrompt, curNorm); slug != "" {
			branch := disambiguate(applyPrefix(prefix, slug), taken)
			return NamingResult{
				Branch:  branch,
				DirName: WorktreeDirName(branch),
				Source:  harnessSourceLabel(ctx.HarnessApp, "prompt"),
				Rule:    "harness_prompt",
			}
		}
	}

	// Tier 5: Known coding agent fallback (<shortAgent>-<adj>-<noun>).
	if shortAgent, ok := knownCodingAgentShort(ctx.HarnessApp); ok {
		branch := pickRandomPair(ctx.Seed, prefix, shortAgent+"-", taken)
		return NamingResult{
			Branch:  branch,
			DirName: WorktreeDirName(branch),
			Source:  "from " + shortAgent,
			Rule:    "harness_agent",
		}
	}

	// Tier 6: Curated random fallback (<adj>-<noun>).
	branch := pickRandomPair(ctx.Seed, prefix, "", taken)
	return NamingResult{
		Branch:  branch,
		DirName: WorktreeDirName(branch),
		Source:  "auto",
		Rule:    "fallback",
	}
}

// SanitizeBranchName converts an arbitrary string into a safe, lowercase Git
// branch name while preserving '/' path segments.
func SanitizeBranchName(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	// Replace backslashes with dashes so they don't act as path separators.
	s = strings.ReplaceAll(s, "@{", "-")
	rawSegs := strings.Split(s, "/")
	outSegs := make([]string, 0, len(rawSegs))
	for _, seg := range rawSegs {
		cleaned := sanitizeSegment(seg)
		if cleaned != "" {
			outSegs = append(outSegs, cleaned)
		}
	}
	res := strings.Join(outSegs, "/")
	if res == "@" {
		return ""
	}
	return res
}

func sanitizeSegment(seg string) string {
	seg = strings.ReplaceAll(seg, "@{", "-")
	var b strings.Builder
	b.Grow(len(seg))
	for _, r := range seg {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", "-")
	}
	s = collapseDashes(s)
	for {
		prev := s
		s = strings.Trim(s, "-.")
		if strings.HasSuffix(s, ".lock") {
			s = strings.TrimSuffix(s, ".lock")
		}
		if s == prev {
			break
		}
	}
	return s
}

// WorktreeDirName converts a Git branch name into a filesystem-safe directory
// basename by replacing '/' with '-', stripping invalid characters, and
// capping length safely.
func WorktreeDirName(branch string) string {
	s := strings.TrimSpace(branch)
	if s == "" {
		return "worktree"
	}
	s = strings.ReplaceAll(s, "/", "-")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", "-")
	}
	out = collapseDashes(out)
	out = strings.Trim(out, "-.")
	if len(out) > maxDirNameLength {
		out = strings.TrimRight(out[:maxDirNameLength], "-.")
	}
	if out == "" {
		return "worktree"
	}
	return out
}

// ValidateBranchName verifies that branch is a safe, valid Git branch ref name.
func ValidateBranchName(branch string) error {
	if branch == "" {
		return errors.New("branch name cannot be empty")
	}
	if branch == "@" {
		return errors.New("branch name cannot be '@'")
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("branch name %q cannot start with '-'", branch)
	}
	if strings.HasPrefix(branch, ".") || strings.HasPrefix(branch, "/") {
		return fmt.Errorf("branch name %q cannot start with '.' or '/'", branch)
	}
	if strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".lock") {
		return fmt.Errorf("branch name %q has invalid suffix", branch)
	}
	if strings.Contains(branch, "..") {
		return fmt.Errorf("branch name %q cannot contain '..'", branch)
	}
	if strings.Contains(branch, "@{") {
		return fmt.Errorf("branch name %q cannot contain '@{'", branch)
	}
	if strings.Contains(branch, "//") {
		return fmt.Errorf("branch name %q cannot contain '//'", branch)
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return fmt.Errorf("branch name %q contains invalid whitespace or control character", branch)
		}
		switch r {
		case '~', '^', ':', '?', '*', '[', '\\':
			return fmt.Errorf("branch name %q contains invalid character %q", branch, r)
		}
	}
	for _, seg := range strings.Split(branch, "/") {
		if seg == "" {
			return fmt.Errorf("branch name %q contains empty path segment", branch)
		}
		if strings.HasPrefix(seg, ".") {
			return fmt.Errorf("branch name %q has segment starting with '.'", branch)
		}
		if strings.HasSuffix(seg, ".lock") {
			return fmt.Errorf("branch name %q has segment ending with '.lock'", branch)
		}
	}
	return nil
}

func resolvePrefix(ctx NamingContext) string {
	raw := strings.TrimSpace(ctx.Prefix)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("TMUX_SPACES_WORKTREE_PREFIX"))
	}
	if raw == "" {
		return ""
	}
	sep := "/"
	switch {
	case strings.HasSuffix(raw, "-"):
		sep = "-"
	case strings.HasSuffix(raw, "_"):
		sep = "_"
	case strings.HasSuffix(raw, "/"):
		sep = "/"
	}
	clean := SanitizeBranchName(raw)
	if clean == "" {
		return ""
	}
	return clean + sep
}

func applyPrefix(prefix, slug string) string {
	if prefix == "" || slug == "" {
		return slug
	}
	if strings.HasPrefix(strings.ToLower(slug), strings.ToLower(prefix)) {
		return slug
	}
	return prefix + slug
}

func isTaken(candidate string, taken map[string]bool) bool {
	if len(taken) == 0 {
		return false
	}
	low := strings.ToLower(candidate)
	if taken[low] {
		return true
	}
	dirLow := strings.ToLower(WorktreeDirName(candidate))
	return taken[dirLow]
}

func disambiguate(base string, taken map[string]bool) string {
	if !isTaken(base, taken) {
		return base
	}
	for i := 2; i <= 9999; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !isTaken(cand, taken) {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d", base, time.Now().Unix()%100000)
}

func pickRandomPair(seed int64, prefix, agentPrefix string, taken map[string]bool) string {
	var state uint64
	if seed == 0 {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err == nil {
			state = binary.LittleEndian.Uint64(buf[:])
		} else {
			state = uint64(time.Now().UnixNano())
		}
	} else {
		state = splitMix64(uint64(seed))
	}

	const total = len(namingAdjectives) * len(namingNouns) // 4096
	startIdx := int(state % uint64(total))
	// 137 is coprime with 4096, so stepping covers distinct adjective/noun pairs.
	const stride = 137
	const maxProbes = 128

	firstBase := ""
	for i := 0; i < maxProbes; i++ {
		idx := (startIdx + i*stride) % total
		adj := namingAdjectives[idx/len(namingNouns)]
		noun := namingNouns[idx%len(namingNouns)]
		base := applyPrefix(prefix, agentPrefix+adj+"-"+noun)
		if i == 0 {
			firstBase = base
		}
		if !isTaken(base, taken) {
			return base
		}
	}
	return disambiguate(firstBase, taken)
}

func splitMix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func targetSourceLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "tab":
		return "from tab name"
	case "space":
		return "from space name"
	default:
		return "from name"
	}
}

func harnessSourceLabel(app, kind string) string {
	if short, ok := knownCodingAgentShort(app); ok {
		return "from " + short + " " + kind
	}
	clean := strings.ToLower(strings.TrimSpace(app))
	if clean != "" && !genericShellNames[clean] {
		if slug := SanitizeBranchName(clean); slug != "" {
			return "from " + slug + " " + kind
		}
	}
	return "from harness " + kind
}

func knownCodingAgentShort(app string) (string, bool) {
	low := strings.ToLower(strings.TrimSpace(app))
	if low == "" || genericShellNames[low] {
		return "", false
	}
	if short, ok := knownCodingAgents[low]; ok {
		return short, true
	}
	return "", false
}

func normalizeCompare(s string) string {
	return SanitizeBranchName(s)
}

func extractTargetSlug(target, repoNorm, curNorm, harnessApp string) string {
	raw := strings.TrimSpace(target)
	if raw == "" {
		return ""
	}
	raw = stripLeadingStatusGlyphs(raw)
	if raw == "" {
		return ""
	}
	low := strings.ToLower(raw)
	if genericShellNames[low] || genericTargetNames[low] || numberedGenRe.MatchString(low) {
		return ""
	}
	if looksLikePath(raw) || versionBannerRe.MatchString(raw) {
		return ""
	}
	// If tab name is simply the running harness name (e.g. "claude"), let
	// harness title/prompt/agent tiers produce a richer name.
	if appLow := strings.ToLower(strings.TrimSpace(harnessApp)); appLow != "" {
		if low == appLow {
			return ""
		}
		if short, ok := knownCodingAgentShort(appLow); ok && low == short {
			return ""
		}
	}

	rawNorm := normalizeCompare(raw)
	if rawNorm == "" || (repoNorm != "" && rawNorm == repoNorm) || (curNorm != "" && rawNorm == curNorm) {
		return ""
	}

	words := tokenizeWords(raw)
	if len(words) == 0 {
		return ""
	}
	// Stop-word filter when phrase has >4 words.
	if len(words) > 4 {
		filtered := filterStopWords(words)
		if len(filtered) > 0 {
			words = filtered
		}
	}
	slug := joinSlugWords(words, maxSlugWords, maxSlugLength)
	if len(slug) < 2 {
		return ""
	}
	if (repoNorm != "" && slug == repoNorm) || (curNorm != "" && slug == curNorm) {
		return ""
	}
	return slug
}

func extractHarnessTitleSlug(title, repoNorm, curNorm string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		return ""
	}
	// Repeatedly strip leading status glyphs and harness prefixes.
	for {
		prev := s
		s = stripLeadingStatusGlyphs(s)
		s = stripHarnessPrefixes(s)
		s = strings.TrimSpace(s)
		if s == prev {
			break
		}
	}
	// Strip trailing " | <cwd>" or " | <repo>" suffix.
	if idx := strings.LastIndex(s, " | "); idx > 0 {
		left := strings.TrimSpace(s[:idx])
		if left != "" {
			s = left
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)
	if genericShellNames[low] || genericTargetNames[low] || numberedGenRe.MatchString(low) {
		return ""
	}
	if _, isAgentOnly := knownCodingAgents[low]; isAgentOnly {
		return ""
	}
	if low == "claude code" || low == "new chat" || low == "new session" {
		return ""
	}
	if looksLikePath(s) || versionBannerRe.MatchString(s) {
		return ""
	}

	words := tokenizeWords(s)
	if len(words) == 0 {
		return ""
	}
	filtered := filterStopWords(words)
	if len(filtered) > 0 {
		words = filtered
	}
	slug := joinSlugWords(words, maxSlugWords, maxSlugLength)
	if len(slug) < 2 {
		return ""
	}
	if curNorm != "" && (slug == curNorm || slug == WorktreeDirName(curNorm)) {
		return ""
	}
	if repoNorm != "" && slug == repoNorm {
		return ""
	}
	return slug
}

func extractHarnessPromptSlug(prompt, curNorm string) string {
	line := firstNonEmptyLine(prompt)
	if line == "" {
		return ""
	}
	// Strip URLs and unwrap markdown links before sentence splitting.
	cleaned := urlRe.ReplaceAllString(line, " ")
	cleaned = mdLinkRe.ReplaceAllString(cleaned, "$1")
	cleaned = stripMarkdownPunctuation(cleaned)

	// Prefer the first sentence, unless it produces fewer than 2 meaningful words.
	sentence := firstSentence(cleaned)
	words := filterStopWords(tokenizeWords(sentence))
	if len(words) < 2 {
		allWords := filterStopWords(tokenizeWords(cleaned))
		if len(allWords) > len(words) {
			words = allWords
		}
	}
	if len(words) == 0 {
		words = tokenizeWords(sentence)
	}
	slug := joinSlugWords(words, maxSlugWords, maxSlugLength)
	if len(slug) < 2 {
		return ""
	}
	if curNorm != "" && (slug == curNorm || slug == WorktreeDirName(curNorm)) {
		return ""
	}
	return slug
}

func stripLeadingStatusGlyphs(s string) string {
	return strings.TrimLeftFunc(s, func(r rune) bool {
		if unicode.IsSpace(r) {
			return true
		}
		// Braille spinner glyphs (U+2800..U+28FF)
		if r >= 0x2800 && r <= 0x28FF {
			return true
		}
		switch r {
		case '✳', '●', '◐', '◑', '◒', '◓', '✓', '✔', '✗', '✘', '○', '◌', '•', '·', '✦', '✧', '⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏', '⠐', '⠂', '⠁', '⠈', '⠠', '⠄':
			return true
		}
		return false
	})
}

var harnessPrefixes = []string{
	"oc | ",
	"oc: ",
	"oc - ",
	"opencode | ",
	"opencode - ",
	"opencode: ",
	"claude code - ",
	"claude code: ",
	"claude code | ",
	"claude - ",
	"claude: ",
	"claude | ",
	"codex - ",
	"codex: ",
	"codex | ",
	"antigravity - ",
	"antigravity: ",
	"antigravity | ",
	"cursor - ",
	"cursor: ",
	"cursor | ",
	"gemini - ",
	"gemini: ",
	"gemini | ",
}

func stripHarnessPrefixes(s string) string {
	low := strings.ToLower(s)
	for _, p := range harnessPrefixes {
		if strings.HasPrefix(low, p) {
			return strings.TrimSpace(s[len(p):])
		}
	}
	return s
}

func looksLikePath(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || s == "~" {
		return true
	}
	if !strings.ContainsAny(s, " \t") && strings.Contains(s, "/") {
		return true
	}
	return false
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "```") {
			continue
		}
		return t
	}
	return ""
}

func stripMarkdownPunctuation(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '`', '*', '_', '#', '>', '~', '[', ']', '(', ')', '{', '}', '"', '\'', '|', '\\':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' || c == '!' || c == '?' {
			// Treat as sentence boundary when at end or followed by whitespace.
			if i+1 == len(s) || unicode.IsSpace(rune(s[i+1])) {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return s
}

func tokenizeWords(s string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return words
}

func filterStopWords(words []string) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) <= 1 || stopWords[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

func joinSlugWords(words []string, maxWords, maxLen int) string {
	var parts []string
	curLen := 0
	for _, w := range words {
		if len(parts) >= maxWords {
			break
		}
		addLen := len(w)
		if len(parts) > 0 {
			addLen++ // hyphen
		}
		if curLen+addLen > maxLen {
			if len(parts) == 0 {
				parts = append(parts, w[:maxLen])
			}
			break
		}
		parts = append(parts, w)
		curLen += addLen
	}
	return SanitizeBranchName(strings.Join(parts, "-"))
}

func collapseDashes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		b.WriteByte(c)
	}
	return b.String()
}
