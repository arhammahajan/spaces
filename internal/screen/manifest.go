// Package screen is a Go port of the Herdr manifest screen detector
// embedded in plugins/zellij-spaces/src/screen (mod.rs, manifest.rs).
//
// It evaluates a captured pane viewport against bundled per-agent manifests
// (manifests.json, embedded with go:embed) to recover Idle / Working /
// Blocked states for panes that do not publish live state. See NOTICE and
// LICENSE-herdr for provenance and licensing.
//
// The evaluator is trimmed to what the Spaces navigator needs: bundled
// manifests, region extraction, gate matching, priority selection, and the
// visible/skip flags. Remote manifest loading, overrides, diagnostics, and
// filesystem/update machinery are intentionally omitted.
package screen

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

//go:embed manifests.json
var bundledManifests string

// defaultRegion is used when a rule omits its region.
const defaultRegion = "whole_recent"

// topNonEmptyLinesEngineVersion is the minimum manifest min_engine_version
// required to use the top_non_empty_lines region.
const topNonEmptyLinesEngineVersion = 3

// manifestFile is the top-level bundle schema.
type manifestFile struct {
	Source        string          `json:"source"`
	EngineVersion uint32          `json:"engine_version"`
	Manifests     []agentManifest `json:"manifests"`
}

// agentManifest is one agent's vendored manifest.
type agentManifest struct {
	ID               string         `json:"id"`
	Version          *string        `json:"version"`
	MinEngineVersion *uint32        `json:"min_engine_version"`
	UpdatedAt        *string        `json:"updated_at"`
	Aliases          []string       `json:"aliases"`
	Rules            []manifestRule `json:"rules"`
}

// Gate is a recursive match gate: every contains/regex/line_regex/all entry
// must match, at least one any entry must match when any is non-empty, and no
// not entry may match. Matching is case-insensitive for contains and
// case-sensitive for regex/line_regex, mirroring the Rust implementation.
type Gate struct {
	All       []Gate   `json:"all"`
	Any       []Gate   `json:"any"`
	Not       []Gate   `json:"not"`
	Contains  []string `json:"contains"`
	Regex     []string `json:"regex"`
	LineRegex []string `json:"line_regex"`
}

// manifestRule is one detection rule: a region plus a match gate.
type manifestRule struct {
	Gate
	ID              string  `json:"id"`
	State           *string `json:"state"`
	Priority        int     `json:"priority"`
	Region          string  `json:"region"`
	VisibleIdle     bool    `json:"visible_idle"`
	VisibleBlocker  bool    `json:"visible_blocker"`
	VisibleWorking  bool    `json:"visible_working"`
	SkipStateUpdate bool    `json:"skip_state_update"`
}

// compiledGate is a Gate with lowercase needles and compiled regexes.
type compiledGate struct {
	all       []*compiledGate
	any       []*compiledGate
	not       []*compiledGate
	contains  []string
	regex     []*regexp.Regexp
	lineRegex []*regexp.Regexp
}

// compiledRule is a rule with its region defaulted and gate compiled.
type compiledRule struct {
	id             string
	state          State
	priority       int
	region         string
	visibleIdle    bool
	visibleBlocker bool
	visibleWorking bool
	skip           bool
	gate           *compiledGate
}

// compiledManifest is one agent's compiled rules.
type compiledManifest struct {
	id    string
	rules []*compiledRule
}

// Engine evaluates screens against compiled manifests.
type Engine struct {
	manifests []*compiledManifest
	lookup    map[string]*compiledManifest
}

// ruleState parses a rule state name, defaulting nil/empty to Unknown.
// Unknown names are rejected so schema drift fails loudly.
func ruleState(s *string) (State, error) {
	if s == nil || *s == "" {
		return StateUnknown, nil
	}
	switch *s {
	case "idle":
		return StateIdle, nil
	case "working":
		return StateWorking, nil
	case "blocked":
		return StateBlocked, nil
	case "unknown":
		return StateUnknown, nil
	default:
		return StateUnknown, fmt.Errorf("unknown state %q", *s)
	}
}

// loadEngine parses and compiles a manifest bundle.
func loadEngine(data string) (*Engine, error) {
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	var file manifestFile
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse manifests: %w", err)
	}
	if len(file.Manifests) == 0 {
		return nil, fmt.Errorf("manifest bundle is empty")
	}
	engine := &Engine{lookup: map[string]*compiledManifest{}}
	for _, manifest := range file.Manifests {
		if len(manifest.Rules) == 0 {
			return nil, fmt.Errorf("manifest %s has no rules", manifest.ID)
		}
		compiled, err := compileManifest(&manifest)
		if err != nil {
			return nil, err
		}
		id := strings.ToLower(manifest.ID)
		if _, dup := engine.lookup[id]; dup {
			return nil, fmt.Errorf("duplicate manifest id %s", id)
		}
		cm := &compiledManifest{id: id, rules: compiled}
		engine.manifests = append(engine.manifests, cm)
		engine.lookup[id] = cm
		for _, alias := range manifest.Aliases {
			key := strings.ToLower(alias)
			if _, exists := engine.lookup[key]; !exists {
				engine.lookup[key] = cm
			}
		}
	}
	return engine, nil
}

func compileManifest(manifest *agentManifest) ([]*compiledRule, error) {
	rules := make([]*compiledRule, 0, len(manifest.Rules))
	for i := range manifest.Rules {
		rule := &manifest.Rules[i]
		region := strings.TrimSpace(rule.Region)
		if region == "" {
			region = defaultRegion
		}
		if !validRegion(region) {
			return nil, fmt.Errorf("rule %s uses invalid region %s", rule.ID, rule.Region)
		}
		if strings.HasPrefix(region, "top_non_empty_lines(") &&
			manifest.MinEngineVersion != nil && *manifest.MinEngineVersion < topNonEmptyLinesEngineVersion {
			return nil, fmt.Errorf("rule %s uses top_non_empty_lines below engine %d",
				rule.ID, topNonEmptyLinesEngineVersion)
		}
		state, err := ruleState(rule.State)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", rule.ID, err)
		}
		gate, err := compileGate(&rule.Gate)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", rule.ID, err)
		}
		rules = append(rules, &compiledRule{
			id:             rule.ID,
			state:          state,
			priority:       rule.Priority,
			region:         region,
			visibleIdle:    rule.VisibleIdle,
			visibleBlocker: rule.VisibleBlocker,
			visibleWorking: rule.VisibleWorking,
			skip:           rule.SkipStateUpdate,
			gate:           gate,
		})
	}
	return rules, nil
}

func compileGate(gate *Gate) (*compiledGate, error) {
	compiled := &compiledGate{}
	for i := range gate.All {
		nested, err := compileGate(&gate.All[i])
		if err != nil {
			return nil, err
		}
		compiled.all = append(compiled.all, nested)
	}
	for i := range gate.Any {
		nested, err := compileGate(&gate.Any[i])
		if err != nil {
			return nil, err
		}
		compiled.any = append(compiled.any, nested)
	}
	for i := range gate.Not {
		nested, err := compileGate(&gate.Not[i])
		if err != nil {
			return nil, err
		}
		compiled.not = append(compiled.not, nested)
	}
	for _, needle := range gate.Contains {
		compiled.contains = append(compiled.contains, strings.ToLower(needle))
	}
	for _, pattern := range gate.Regex {
		re, err := compilePattern(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex %q: %w", pattern, err)
		}
		compiled.regex = append(compiled.regex, re)
	}
	for _, pattern := range gate.LineRegex {
		re, err := compilePattern(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid line_regex %q: %w", pattern, err)
		}
		compiled.lineRegex = append(compiled.lineRegex, re)
	}
	return compiled, nil
}

// compilePattern compiles a manifest pattern after translating Rust-regex
// escapes that Go's RE2 engine rejects:
//
//   - \uHHHH / \UHHHHHHHH / \u{H...} (Rust unicode escapes) become \x{...}
//     (same code points; RE2 has no \u escape).
//   - \p{Alphabetic} becomes \pL. RE2 supports the Letter category but not
//     the Alphabetic binary property; spinner lines only need letters.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(translatePattern(pattern))
}

// translatePattern rewrites Rust-only escapes into RE2 equivalents. Escaped
// backslashes (\\) are preserved so literal "\u1234" text still means a
// literal backslash followed by "u1234".
func translatePattern(pattern string) string {
	if !strings.Contains(pattern, `\u`) && !strings.Contains(pattern, `\U`) &&
		!strings.Contains(pattern, `\p{Alphabetic}`) {
		return pattern
	}
	var b strings.Builder
	b.Grow(len(pattern))
	i := 0
	for i < len(pattern) {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			next := pattern[i+1]
			switch {
			case next == '\\':
				b.WriteString(`\\`)
				i += 2
				continue
			case next == 'p' && strings.HasPrefix(pattern[i:], `\p{Alphabetic}`):
				b.WriteString(`\pL`)
				i += len(`\p{Alphabetic}`)
				continue
			case next == 'u' || next == 'U':
				if text, size := unicodeEscape(pattern, i); size > 0 {
					b.WriteString(text)
					i += size
					continue
				}
			}
		}
		b.WriteByte(pattern[i])
		i++
	}
	return b.String()
}

// unicodeEscape translates a Rust \uHHHH, \UHHHHHHHH, or \u{H...} escape at
// s[offset:] into RE2 \x{...} form, returning the replacement and its source
// length. It returns size 0 when there is no valid escape at offset.
func unicodeEscape(s string, offset int) (string, int) {
	// offset points at the backslash; s[offset+1] is 'u' or 'U'.
	rest := s[offset+1:]
	if strings.HasPrefix(rest, "u{") {
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			return "", 0
		}
		digits := rest[2:end]
		if len(digits) == 0 || len(digits) > 6 {
			return "", 0
		}
		for i := 0; i < len(digits); i++ {
			if !isHexByte(digits[i]) {
				return "", 0
			}
		}
		return `\x{` + digits + `}`, end + 2
	}
	size := 4
	if rest[0] == 'U' {
		size = 8
	}
	if !isHexRun(s, offset+2, size) {
		return "", 0
	}
	return `\x{` + s[offset+2:offset+2+size] + `}`, 2 + size
}

func isHexByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// isHexRun reports whether s[offset:offset+n] is all ASCII hex digits.
func isHexRun(s string, offset, n int) bool {
	if offset+n > len(s) {
		return false
	}
	for _, c := range s[offset : offset+n] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// manifest returns the compiled manifest covering app (already normalized).
func (e *Engine) manifest(app string) *compiledManifest {
	return e.lookup[app]
}

// manifestID returns the canonical manifest id covering app, if any.
func (e *Engine) manifestID(app string) string {
	if m := e.manifest(app); m != nil {
		return m.id
	}
	return ""
}

// detect evaluates screen for an already-normalized app label.
func (e *Engine) detect(app, screen, oscTitle, oscProgress string) Detected {
	manifest := e.manifest(app)
	if manifest == nil {
		return unknownDetected("")
	}
	input := detectionInput{screen: screen, oscTitle: oscTitle, oscProgress: oscProgress}
	var matched *compiledRule
	for _, rule := range manifest.rules {
		if !rule.gate.matches(region(input, rule.region)) {
			continue
		}
		if matched != nil && matched.priority >= rule.priority {
			continue
		}
		matched = rule
	}
	if matched == nil {
		// A known agent with no matching rule yields a low-confidence
		// Unknown: the caller only applies Idle when visible_idle is set,
		// so this behaves as "no fresh evidence" rather than asserting a state.
		return unknownDetected("")
	}
	return Detected{
		State:           matched.state,
		VisibleIdle:     matched.visibleIdle && matched.state == StateIdle,
		VisibleBlocker:  matched.visibleBlocker && matched.state == StateBlocked,
		VisibleWorking:  matched.visibleWorking && matched.state == StateWorking,
		SkipStateUpdate: matched.skip,
		MatchedRule:     matched.id,
	}
}

// matches evaluates a compiled gate against region text.
func (g *compiledGate) matches(text string) bool {
	lower := strings.ToLower(text)
	return g.matchesLower(text, lower)
}

func (g *compiledGate) matchesLower(text, lower string) bool {
	for _, needle := range g.contains {
		if !strings.Contains(lower, needle) {
			return false
		}
	}
	for _, re := range g.regex {
		if !re.MatchString(text) {
			return false
		}
	}
	for _, re := range g.lineRegex {
		matched := false
		for _, line := range strings.Split(text, "\n") {
			if re.MatchString(line) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, nested := range g.all {
		if !nested.matchesLower(text, lower) {
			return false
		}
	}
	if len(g.any) > 0 {
		any := false
		for _, nested := range g.any {
			if nested.matchesLower(text, lower) {
				any = true
				break
			}
		}
		if !any {
			return false
		}
	}
	for _, nested := range g.not {
		if nested.matchesLower(text, lower) {
			return false
		}
	}
	return true
}

// detectionInput carries the screen and out-of-band title signals.
type detectionInput struct {
	screen      string
	oscTitle    string
	oscProgress string
}

func validRegion(spec string) bool {
	trimmed := strings.TrimSpace(spec)
	switch trimmed {
	case "whole_recent",
		"after_last_prompt_marker",
		"before_current_prompt_marker",
		"whole_recent_without_current_prompt_marker",
		"current_prompt_block_marker",
		"after_current_prompt_block_marker",
		"prompt_box_body",
		"above_prompt_box",
		"last_non_empty_above_prompt_box",
		"after_last_horizontal_rule",
		"osc_title",
		"osc_progress":
		return true
	}
	if regionCount(trimmed, "bottom_lines") != nil {
		return true
	}
	if regionCount(trimmed, "bottom_non_empty_lines") != nil {
		return true
	}
	return topRegionCount(trimmed) != nil
}

// region extracts the named viewport section.
func region(input detectionInput, spec string) string {
	trimmed := strings.TrimSpace(spec)
	switch trimmed {
	case "osc_title":
		return input.oscTitle
	case "osc_progress":
		return input.oscProgress
	}
	content := input.screen
	switch trimmed {
	case "whole_recent":
		return content
	case "after_last_prompt_marker":
		return afterLastPromptMarker(content)
	case "before_current_prompt_marker":
		return beforeCurrentPromptMarker(content)
	case "whole_recent_without_current_prompt_marker":
		return wholeRecentWithoutCurrentPromptMarker(content)
	case "current_prompt_block_marker":
		text, ok := currentPromptBlockMarker(content)
		if !ok {
			return ""
		}
		return text
	case "after_current_prompt_block_marker":
		text, ok := afterCurrentPromptBlockMarker(content)
		if !ok {
			return ""
		}
		return text
	case "prompt_box_body":
		text, ok := promptBoxBody(content)
		if !ok {
			return ""
		}
		return text
	case "above_prompt_box":
		return abovePromptBox(content)
	case "last_non_empty_above_prompt_box":
		return lastNonEmptyLine(abovePromptBox(content))
	case "after_last_horizontal_rule":
		return afterLastHorizontalRule(content)
	}
	if count := regionCount(trimmed, "bottom_lines"); count != nil {
		return bottomLines(content, *count)
	}
	if count := regionCount(trimmed, "bottom_non_empty_lines"); count != nil {
		return bottomNonEmptyLines(content, *count)
	}
	if count := topRegionCount(trimmed); count != nil {
		return topNonEmptyLines(content, *count)
	}
	return ""
}

// regionCount parses "name(N)" counts; bottom_lines(0) is valid.
func regionCount(spec, name string) *int {
	rest, ok := strings.CutPrefix(spec, name)
	if !ok {
		return nil
	}
	rest, ok = strings.CutPrefix(rest, "(")
	if !ok {
		return nil
	}
	rest, ok = strings.CutSuffix(rest, ")")
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

// topRegionCount parses "top_non_empty_lines(N)" with the engine's strict
// validation: no leading zeros, all digits, at most u16::MAX.
func topRegionCount(spec string) *int {
	rest, ok := strings.CutPrefix(spec, "top_non_empty_lines")
	if !ok {
		return nil
	}
	rest, ok = strings.CutPrefix(rest, "(")
	if !ok {
		return nil
	}
	rest, ok = strings.CutSuffix(rest, ")")
	if !ok {
		return nil
	}
	if rest == "" || strings.HasPrefix(rest, "0") {
		return nil
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return nil
		}
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n > 65535 {
		return nil
	}
	return &n
}

// textLine is one screen line with byte offsets into the content.
type textLine struct {
	text  string // without trailing \n or \r
	start int    // byte offset of the line start
	end   int    // byte offset just past the \n, or len(content)
}

// splitTextLines mirrors Rust str::lines: split on \n, drop a single
// trailing empty segment, strip \r.
func splitTextLines(content string) []textLine {
	var lines []textLine
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, textLine{
				text:  strings.TrimSuffix(content[start:i], "\r"),
				start: start,
				end:   i + 1,
			})
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, textLine{
			text:  strings.TrimSuffix(content[start:], "\r"),
			start: start,
			end:   len(content),
		})
	}
	return lines
}

func bottomLines(content string, count int) string {
	lines := splitTextLines(content)
	start := len(lines) - count
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		return ""
	}
	return content[lines[start].start:]
}

func bottomNonEmptyLines(content string, count int) string {
	lines := splitTextLines(content)
	// Take the last `count` non-empty lines (or all of them when fewer
	// exist) and return from the earliest one, mirroring
	// `filter(non_empty).take(count).last()`.
	start := -1
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < count; i-- {
		if strings.TrimSpace(lines[i].text) != "" {
			seen++
			start = i
		}
	}
	if start < 0 {
		return ""
	}
	return content[lines[start].start:]
}

func topNonEmptyLines(content string, count int) string {
	lines := splitTextLines(content)
	// Take the first `count` non-empty lines (or all of them when fewer
	// exist) and return through the last one taken.
	end := -1
	seen := 0
	for _, line := range lines {
		if seen >= count {
			break
		}
		if strings.TrimSpace(line.text) != "" {
			seen++
			end = line.end
		}
	}
	if end < 0 {
		return ""
	}
	return content[:end]
}

func lineTexts(lines []textLine) []string {
	texts := make([]string, len(lines))
	for i, line := range lines {
		texts[i] = line.text
	}
	return texts
}

func afterLastPromptMarker(content string) string {
	lines := splitTextLines(content)
	index := -1
	for i, line := range lines {
		if codexPromptLine(line.text) {
			index = i
		}
	}
	if index < 0 {
		return content
	}
	if index+1 >= len(lines) {
		return ""
	}
	return content[lines[index+1].start:]
}

func beforeCurrentPromptMarker(content string) string {
	lines := splitTextLines(content)
	index := currentCodexPromptIndex(lineTexts(lines))
	if index < 0 {
		return content
	}
	end := lines[index].start
	if end > len(content) {
		end = len(content)
	}
	return content[:end]
}

func wholeRecentWithoutCurrentPromptMarker(content string) string {
	lines := splitTextLines(content)
	if currentCodexPromptIndex(lineTexts(lines)) >= 0 {
		return ""
	}
	return content
}

func currentPromptBlockMarker(content string) (string, bool) {
	lines := splitTextLines(content)
	prompt := currentCodexPromptIndex(lineTexts(lines))
	if prompt < 0 {
		return "", false
	}
	for i := prompt - 1; i >= 0; i-- {
		if codexBlockMarkerLine(lines[i].text) {
			return lines[i].text, true
		}
	}
	return "", false
}

func afterCurrentPromptBlockMarker(content string) (string, bool) {
	lines := splitTextLines(content)
	prompt := currentCodexPromptIndex(lineTexts(lines))
	if prompt < 0 {
		return "", false
	}
	block := -1
	for i := prompt - 1; i >= 0; i-- {
		if codexBlockMarkerLine(lines[i].text) {
			block = i
			break
		}
	}
	if block < 0 {
		return "", false
	}
	return content[lines[block].start:], true
}

// currentCodexPromptIndex returns the last codex prompt line that has no
// block-marker line after it, or -1 when there is none.
func currentCodexPromptIndex(lines []string) int {
	prompt := -1
	for i, line := range lines {
		if codexPromptLine(line) {
			prompt = i
		}
	}
	if prompt < 0 {
		return -1
	}
	for _, line := range lines[prompt+1:] {
		if codexBlockMarkerLine(line) {
			return -1
		}
	}
	return prompt
}

func codexPromptLine(line string) bool {
	return line == "›" || strings.HasPrefix(line, "› ")
}

func codexBlockMarkerLine(line string) bool {
	for _, prefix := range []string{"•", "■", "✗", "✓"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func promptBoxBody(content string) (string, bool) {
	lines := splitTextLines(content)
	top, ok := promptBoxTopBorderIndex(lines)
	if !ok {
		return "", false
	}
	start := len(content)
	if top+1 < len(lines) {
		start = lines[top+1].start
	}
	end := len(content)
	for i := top + 1; i < len(lines); i++ {
		if isHorizontalRule(lines[i].text) {
			end = lines[i].start
			break
		}
	}
	if start > end {
		start = end
	}
	return content[start:end], true
}

func abovePromptBox(content string) string {
	lines := splitTextLines(content)
	top, ok := promptBoxTopBorderIndex(lines)
	if !ok {
		return content
	}
	end := lines[top].start
	if end > len(content) {
		end = len(content)
	}
	return content[:end]
}

func afterLastHorizontalRule(content string) string {
	lastEnd := 0
	for _, line := range splitTextLines(content) {
		if isHorizontalRule(line.text) {
			lastEnd = line.end
		}
	}
	if lastEnd > len(content) {
		lastEnd = len(content)
	}
	return content[lastEnd:]
}

func lastNonEmptyLine(content string) string {
	lines := splitTextLines(content)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i].text) != "" {
			return lines[i].text
		}
	}
	return ""
}

// promptBoxTopBorderIndex finds the second horizontal rule scanning from the
// bottom, mirroring the Rust implementation.
func promptBoxTopBorderIndex(lines []textLine) (int, bool) {
	borderCount := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if isHorizontalRule(lines[i].text) {
			borderCount++
			if borderCount == 2 {
				return i, true
			}
		}
	}
	return -1, false
}

// isHorizontalRule mirrors the Rust check: a run of leading ─ with an empty
// (modulo whitespace) suffix, or a run of at least 3 with any suffix.
func isHorizontalRule(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	rest := trimmed
	count := 0
	for strings.HasPrefix(rest, "─") {
		rest = strings.TrimPrefix(rest, "─")
		count++
	}
	if count == 0 {
		return false
	}
	suffix := strings.TrimLeftFunc(rest, unicode.IsSpace)
	return suffix == "" || count >= 3
}

var (
	defaultEngineOnce sync.Once
	defaultEngine     *Engine
)

// engine returns the compiled bundled engine, falling back to an empty
// engine (never nil) when the bundle is broken.
func engine() *Engine {
	defaultEngineOnce.Do(func() {
		loaded, err := loadEngine(bundledManifests)
		if err != nil {
			fmt.Fprintln(os.Stderr, "spaces: screen manifests unavailable:", err)
			loaded = &Engine{lookup: map[string]*compiledManifest{}}
		}
		defaultEngine = loaded
	})
	return defaultEngine
}

// normalizeApp lowercases, takes the basename, strips common executable
// suffixes and versioned binary suffixes (e.g. "muse-bin-1.4.24-R9131.1" ->
// "muse"), mirroring the Rust implementation.
func normalizeApp(app string) string {
	trimmed := strings.TrimSpace(app)
	base := strings.TrimSuffix(trimmed, "/")
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if base == "" {
		base = trimmed
	}
	lowered := strings.ToLower(base)
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".ps1", ".js", ".mjs", ".cjs"} {
		if stripped, ok := strings.CutSuffix(lowered, suffix); ok {
			lowered = stripped
			break
		}
	}
	if prefix, _, ok := strings.Cut(lowered, "-bin-"); ok && prefix != "" {
		return prefix
	}
	return lowered
}
