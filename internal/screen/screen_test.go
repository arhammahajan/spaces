package screen

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// bundleManifests parses the embedded manifests.json without compiling,
// for audit tests.
func bundleManifests(t *testing.T) manifestFile {
	t.Helper()
	var file manifestFile
	dec := json.NewDecoder(strings.NewReader(bundledManifests))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatalf("bundled manifests do not parse: %v", err)
	}
	return file
}

func TestBundledManifestsLoad(t *testing.T) {
	file := bundleManifests(t)
	if len(file.Manifests) != 22 {
		t.Fatalf("got %d manifests, want 22", len(file.Manifests))
	}
	eng := engine()
	if len(eng.manifests) != 22 {
		t.Fatalf("engine has %d manifests, want 22", len(eng.manifests))
	}
	for _, id := range []string{"claude", "agy", "cursor", "codex", "opencode", "copilot", "gemini", "amp"} {
		if eng.lookup[id] == nil {
			t.Errorf("missing manifest %q", id)
		}
	}
	// Aliases resolve to the same manifest.
	if engine().manifest("claude-code") != engine().manifest("claude") {
		t.Error("claude-code should resolve to claude")
	}
	if engine().manifest("cursor-agent") != engine().manifest("cursor") {
		t.Error("cursor-agent should resolve to cursor")
	}
	if engine().manifest("antigravity") != engine().manifest("agy") {
		t.Error("antigravity should resolve to agy")
	}
	if engine().manifest("github-copilot") != engine().manifest("copilot") {
		t.Error("github-copilot should resolve to copilot")
	}
}

// TestEveryPatternCompiles audits every regex/line_regex in the bundle,
// including nested gates, against RE2. translatePattern must cover any
// Rust-only syntax; anything left uncompilable fails here by name.
func TestEveryPatternCompiles(t *testing.T) {
	file := bundleManifests(t)
	var bad []string
	var checked int
	var walk func(gate *Gate, where string)
	walk = func(gate *Gate, where string) {
		for _, p := range gate.Regex {
			checked++
			if _, err := compilePattern(p); err != nil {
				bad = append(bad, where+" regex "+p+": "+err.Error())
			}
		}
		for _, p := range gate.LineRegex {
			checked++
			if _, err := compilePattern(p); err != nil {
				bad = append(bad, where+" line_regex "+p+": "+err.Error())
			}
		}
		for _, n := range gate.All {
			n := n
			walk(&n, where+".all")
		}
		for _, n := range gate.Any {
			n := n
			walk(&n, where+".any")
		}
		for _, n := range gate.Not {
			n := n
			walk(&n, where+".not")
		}
	}
	for i := range file.Manifests {
		m := &file.Manifests[i]
		if len(m.Rules) == 0 {
			t.Errorf("manifest %s has no rules", m.ID)
		}
		for j := range m.Rules {
			walk(&m.Rules[j].Gate, m.ID+"/"+m.Rules[j].ID)
		}
	}
	if checked == 0 {
		t.Fatal("no patterns checked")
	}
	t.Logf("checked %d patterns", checked)
	for _, b := range bad {
		t.Errorf("uncompilable pattern: %s", b)
	}
}

func TestTranslatePattern(t *testing.T) {
	cases := map[string]string{
		`^\s*[\u2801-\u28FF]+\s+\S`: `^\s*[\x{2801}-\x{28FF}]+\s+\S`,
		`\p{Alphabetic}+\w*ing\b`:   `\pL+\w*ing\b`,
		`\\u2800 literal`:           `\\u2800 literal`, // escaped backslash untouched
		`plain (?:x|y)\s*\z`:        `plain (?:x|y)\s*\z`,
		`short \u12 tail`:           `short \u12 tail`, // too few digits: left alone (compile error surfaces)
		`big \U0001F600 end`:        `big \x{0001F600} end`,
		`mark \u{fe0f}? end`:        `mark \x{fe0f}? end`,
		`bad \u{xyz} end`:           `bad \u{xyz} end`, // invalid braced: left alone (compile error surfaces)
		`\p{Alphabetic}`:            `\pL`,
		`\p{L}+`:                    `\p{L}+`,
	}
	for in, want := range cases {
		if got := translatePattern(in); got != want {
			t.Errorf("translatePattern(%q) = %q, want %q", in, got, want)
		}
	}
	// Translated spinner patterns must compile and match braille spinners.
	for _, p := range []string{`^\s*[\u2801-\u28FF]+\s+\S`, `^\s*(⬡|⬢|[\u2800-\u28FF]+)\s+\p{Alphabetic}+\w*ing\b`} {
		re, err := compilePattern(p)
		if err != nil {
			t.Fatalf("compilePattern(%q): %v", p, err)
		}
		if !re.MatchString("⠋ Working on it") {
			t.Errorf("%q should match braille spinner line", p)
		}
	}
	if _, err := regexp.Compile(translatePattern(`^\s*[\u2801-\u28FF]+\s+\S`)); err != nil {
		t.Fatalf("translated pattern should be RE2-clean: %v", err)
	}
}

func TestStateString(t *testing.T) {
	cases := map[State]string{
		StateIdle: "idle", StateWorking: "working", StateBlocked: "blocked", StateUnknown: "unknown",
		State(99): "unknown",
	}
	for s, want := range cases {
		if s.String() != want {
			t.Errorf("State(%d) = %q, want %q", int(s), s.String(), want)
		}
	}
}

func TestCanonicalApp(t *testing.T) {
	cases := map[string]string{
		"claude-code":                "claude",
		"claude":                     "claude",
		"cursor-agent":               "cursor",
		"antigravity":                "agy",
		"github-copilot":             "copilot",
		"dsh-tui":                    "deepseek",
		"dsh":                        "deepseek",
		"fx-router":                  "fx",
		"vibe-acp":                   "vibe",
		"kimi-cli":                   "kimi",
		"kimi-code":                  "kimi",
		"deepseek":                   "deepseek",
		"some-unknown":               "some-unknown",
		"/usr/local/bin/claude-code": "claude",
		"CLAUDE-CODE":                "claude",
		"opencode.exe":               "opencode",
		"codex.js":                   "codex",
		"foo/":                       "foo",
	}
	for in, want := range cases {
		if got := CanonicalApp(in); got != want {
			t.Errorf("CanonicalApp(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentIsKnown(t *testing.T) {
	for _, app := range []string{"claude", "claude-code", "cursor-agent", "gemini", "codex", "opencode", "amp", "antigravity"} {
		if !AgentIsKnown(app) {
			t.Errorf("AgentIsKnown(%q) = false", app)
		}
	}
	for _, app := range []string{"deepseek", "dsh-tui", "some-unknown-app", ""} {
		if AgentIsKnown(app) {
			t.Errorf("AgentIsKnown(%q) = true, want false", app)
		}
	}
}

func TestOSCAuthority(t *testing.T) {
	for _, app := range []string{
		"claude-code", "mcode", "fx-router", "vibe-acp", "dsh-tui",
		"deepseek", "muse", "codex", "opencode", "pi", "crush", "grok",
	} {
		if !OSCAuthority(app) {
			t.Errorf("OSCAuthority(%q) = false, want true", app)
		}
	}
	for _, app := range []string{"cursor-agent", "cursor", "gemini", "amp", "cline", "kilo", "unknown-x"} {
		if OSCAuthority(app) {
			t.Errorf("OSCAuthority(%q) = true, want false", app)
		}
	}
}

func TestDetectEmptyScreens(t *testing.T) {
	for _, screen := range []string{"", "   ", "\n\n  \n", "\r\n"} {
		d := Detect("claude", screen, "")
		if d.State != StateUnknown || d.HasEvidence() || d.MatchedRule != "" {
			t.Errorf("Detect(claude, %q) = %+v, want empty Unknown", screen, d)
		}
	}
}

func TestDetectUnknownApp(t *testing.T) {
	d := Detect("some-unknown-app", "● Working on the task\n  ⎿  running tests\n\n───\n❯\n", "")
	if d.State != StateUnknown || d.HasEvidence() {
		t.Errorf("got %+v", d)
	}
}

func TestGeminiBlocker(t *testing.T) {
	screen := "│ Apply this change\n│ Allow execution\n│ Do you want to proceed?\n"
	d := Detect("gemini", screen, "")
	if d.State != StateBlocked || !d.VisibleBlocker {
		t.Errorf("got %+v", d)
	}
}

func TestCursorBlocker(t *testing.T) {
	screen := "Waiting for approval to run this command?\n  Run (once) (y)\n  Skip (esc or n)\n"
	d := Detect("cursor-agent", screen, "")
	if d.State != StateBlocked || !d.VisibleBlocker {
		t.Errorf("got %+v", d)
	}
}

func TestClaudeWorkingAndBlocked(t *testing.T) {
	working := "● Extracting skill names from upstream trees\n" +
		"  ⎿  $ cd /tmp\n" +
		" \n" +
		"✽ Pontificating… (2m 12s · ↓ 4.3k tokens)\n" +
		" \n" +
		"───────────────\n" +
		"❯\n" +
		"───────────────\n" +
		"  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt\n"
	d := Detect("claude-code", working, "")
	if d.State != StateWorking || !d.VisibleWorking {
		t.Errorf("working: got %+v", d)
	}

	blocked := "──────────────────────────────\n" +
		" Allow this read outside the working directories?\n" +
		" ❯ 1. Yes, and keep allowing any reads\n" +
		"   2. No, and block reads\n" +
		" \n" +
		" Esc to cancel · Tab to amend\n"
	d = Detect("claude-code", blocked, "")
	if d.State != StateBlocked {
		t.Errorf("blocked: got %+v", d)
	}
	// The legacy Claude rule does not set the strong visible_blocker flag.
}

func TestPlainShellNoAgent(t *testing.T) {
	d := Detect("gemini", "$ ls\nCargo.toml  src\n$ ", "")
	if d.State != StateUnknown || d.HasEvidence() {
		t.Errorf("got %+v", d)
	}
}

func TestAmpOSCTitle(t *testing.T) {
	d := Detect("amp", "some viewport text\n", "⠋ working on task")
	if d.State != StateWorking || !d.VisibleWorking {
		t.Errorf("osc working: got %+v", d)
	}
	d = Detect("amp", "some viewport text\n", "Plugin confirmation needed: edit file?")
	if d.State != StateBlocked || !d.VisibleBlocker {
		t.Errorf("osc blocked: got %+v", d)
	}
	// Blocker outranks the spinner even when both match.
	d = Detect("amp", "some viewport text\n", "⠋ Plugin confirmation needed")
	if d.State != StateBlocked {
		t.Errorf("priority: got %+v", d)
	}
}

func TestCodexPromptMarkerRegions(t *testing.T) {
	screen := "› do the thing\n• Running tests\npress enter to confirm or esc to cancel\n"
	d := Detect("codex", screen, "")
	if d.State != StateBlocked || !d.VisibleBlocker {
		t.Errorf("after_last_prompt_marker: got %+v", d)
	}
	// Transcript viewer content above the marker sets skip without state.
	screen2 := "› earlier\n↑/↓ to scroll pgup/pgdn to home/end to jump q to quit esc to edit prev\n"
	eng := engine()
	d2 := eng.detect("codex", screen2, "", "")
	if !d2.SkipStateUpdate || d2.State != StateUnknown {
		t.Errorf("skip: got %+v", d2)
	}
}

func TestOpencodePermissionBlocked(t *testing.T) {
	screen := "△ Permission required\n↑↓ select · enter confirm · esc dismiss\nedit src/main.rs\n"
	d := Detect("opencode", screen, "")
	if d.State != StateBlocked || !d.VisibleBlocker {
		t.Errorf("got %+v", d)
	}
}

func TestGenericNoiseIsUnknown(t *testing.T) {
	noise := "hello world\njust a plain shell with $ prompts\nand some output 12345\n"
	file := bundleManifests(t)
	for _, m := range file.Manifests {
		if m.ID == "cline" {
			// cline ships a deliberate catch-all (regex (?s).+, priority -10)
			// so plain text reads as working there; the Rust engine agrees.
			continue
		}
		d := Detect(m.ID, noise, "")
		if d.State != StateUnknown || d.HasEvidence() {
			t.Errorf("%s: noise detected as %+v", m.ID, d)
		}
	}
}

func TestPriorityGatesAndNotGates(t *testing.T) {
	const bundle = `{
      "source":"test","engine_version":3,
      "manifests":[{"id":"claude","rules":[
        {"id":"low","state":"idle","priority":1,"contains":["match"]},
        {"id":"high","state":"working","priority":10,"contains":["match"],
         "all":[{"any":[{"regex":["w[io]n"]},{"contains":["fallback"]}]}],
         "not":[{"contains":["blocked"]}]},
        {"id":"tie","state":"blocked","priority":10,"visible_blocker":true,"contains":["match","tiebreak"]},
        {"id":"line","state":"blocked","priority":20,"visible_blocker":true,"line_regex":["^exact line$"]}
      ]}]
    }`
	eng, err := loadEngine(bundle)
	if err != nil {
		t.Fatal(err)
	}
	high := eng.detect("claude", "match win", "", "")
	if high.State != StateWorking || high.MatchedRule != "high" {
		t.Errorf("got %+v", high)
	}
	blocked := eng.detect("claude", "match win blocked", "", "")
	if blocked.State != StateIdle {
		t.Errorf("not-gate: got %+v", blocked)
	}
	// Tie at priority 10: first rule wins.
	tie := eng.detect("claude", "match win tiebreak", "", "")
	if tie.State != StateWorking || tie.MatchedRule != "high" {
		t.Errorf("tie: got %+v", tie)
	}
	line := eng.detect("claude", "before\nexact line\nafter", "", "")
	if line.State != StateBlocked || !line.VisibleBlocker {
		t.Errorf("line: got %+v", line)
	}
	// Case-insensitive contains vs case-sensitive regex.
	if !eng.detect("claude", "MATCH win", "", "").HasEvidence() && eng.detect("claude", "MATCH win", "", "").State != StateWorking {
		t.Errorf("contains should be case-insensitive: %+v", eng.detect("claude", "MATCH win", "", ""))
	}
	// Case-insensitive contains vs case-sensitive regex: "match WIN" fails
	// the high rule's regex but still matches the low rule's contains.
	if got := eng.detect("claude", "match WIN", "", ""); got.State != StateIdle || got.MatchedRule != "low" {
		t.Errorf("regex should be case-sensitive, got %+v", got)
	}
}

func TestRegionsExtractSections(t *testing.T) {
	in := func(s string) detectionInput { return detectionInput{screen: s} }
	screen := "top\n───\n│ a │\n───\nlast\n"
	if got := region(in(screen), "bottom_lines(2)"); got != "───\nlast\n" {
		t.Errorf("bottom_lines(2) = %q", got)
	}
	if got := region(in(screen), "bottom_non_empty_lines(1)"); got != "last\n" {
		t.Errorf("bottom_non_empty_lines(1) = %q", got)
	}
	if got := region(in("one\n\n\ntwo\n"), "bottom_non_empty_lines(2)"); got != "one\n\n\ntwo\n" {
		t.Errorf("bottom_non_empty_lines(2) = %q", got)
	}
	if got := region(in(screen), "bottom_lines(0)"); got != "" {
		t.Errorf("bottom_lines(0) = %q", got)
	}
	if got := region(in(screen), "bottom_lines(99)"); got != screen {
		t.Errorf("bottom_lines(99) = %q", got)
	}
}

func TestTopNonEmptyLinesRegion(t *testing.T) {
	in := func(s string) detectionInput { return detectionInput{screen: s} }
	screen := "first\n\nsecond\nthird\n"
	if got := region(in(screen), "top_non_empty_lines(2)"); got != "first\n\nsecond\n" {
		t.Errorf("got %q", got)
	}
	if got := region(in(screen), "top_non_empty_lines(01)"); got != "" {
		t.Errorf("leading zero should be invalid, got %q", got)
	}
	// Engine version gate.
	const bundle = `{"source":"t","engine_version":3,"manifests":[
      {"id":"x","min_engine_version":2,"rules":[{"id":"r","region":"top_non_empty_lines(5)","contains":["a"]}]},
      {"id":"y","rules":[{"id":"r","region":"top_non_empty_lines(5)","contains":["a"]}]}]}`
	eng, err := loadEngine(bundle)
	if err == nil || !strings.Contains(err.Error(), "top_non_empty_lines") {
		t.Errorf("expected engine-version error, got %v", eng)
	}
	const okBundle = `{"source":"t","engine_version":3,"manifests":[
      {"id":"y","rules":[{"id":"r","region":"top_non_empty_lines(5)","contains":["a"]}]}]}`
	if _, err := loadEngine(okBundle); err != nil {
		t.Errorf("no min_engine_version should be fine: %v", err)
	}
}

func TestOSCTitleRegions(t *testing.T) {
	withTitle := detectionInput{screen: "screen text\n", oscTitle: "✳ working", oscProgress: "50%"}
	if got := region(withTitle, "osc_title"); got != "✳ working" {
		t.Errorf("got %q", got)
	}
	if got := region(withTitle, "osc_progress"); got != "50%" {
		t.Errorf("got %q", got)
	}
}

func TestHorizontalRuleRegions(t *testing.T) {
	in := func(s string) detectionInput { return detectionInput{screen: s} }
	screen := "work output\n───\n│ prompt │\n───\n❯ input\n"
	if got := region(in(screen), "prompt_box_body"); got != "│ prompt │\n" {
		t.Errorf("prompt_box_body = %q", got)
	}
	if got := region(in(screen), "above_prompt_box"); got != "work output\n" {
		t.Errorf("above_prompt_box = %q", got)
	}
	if got := region(in(screen), "last_non_empty_above_prompt_box"); got != "work output" {
		t.Errorf("last_non_empty = %q", got)
	}
	if got := region(in(screen), "after_last_horizontal_rule"); got != "❯ input\n" {
		t.Errorf("after_last_rule = %q", got)
	}
	if got := region(in("no rules here\n"), "prompt_box_body"); got != "" {
		t.Errorf("expected empty without rules")
	}
	if got := region(in("no rules here\n"), "after_last_horizontal_rule"); got != "no rules here\n" {
		t.Errorf("got %q", got)
	}
}

func TestPromptMarkerRegions(t *testing.T) {
	in := func(s string) detectionInput { return detectionInput{screen: s} }
	screen := "old\n› prompt one\n• did x\n› current\noutput\n"
	if got := region(in(screen), "after_last_prompt_marker"); got != "output\n" {
		t.Errorf("after_last = %q", got)
	}
	if got := region(in(screen), "before_current_prompt_marker"); got != "old\n› prompt one\n• did x\n" {
		t.Errorf("before_current = %q", got)
	}
	if got := region(in(screen), "whole_recent_without_current_prompt_marker"); got != "" {
		t.Errorf("whole_without = %q", got)
	}
	if got := region(in(screen), "current_prompt_block_marker"); got != "• did x" {
		t.Errorf("nearest block marker above current prompt, got %q", got)
	}
	screen2 := "old\n› current\n"
	if got := region(in(screen2), "whole_recent_without_current_prompt_marker"); got != "" {
		t.Errorf("whole_without with live prompt = %q, want empty", got)
	}
	screen3 := "› do it\n• Running\n✓ Done\n› next\ntail\n"
	if got, ok := currentPromptBlockMarker(screen3); !ok || got != "✓ Done" {
		t.Errorf("block marker = %q, %v", got, ok)
	}
	if got, ok := afterCurrentPromptBlockMarker(screen3); !ok || !strings.HasPrefix(got, "✓ Done") {
		t.Errorf("after block = %q, %v", got, ok)
	}
	// A block marker after the prompt voids the "current" prompt.
	if idx := currentCodexPromptIndex(strings.Split("› a\n• b\n› c\n• d\n", "\n")); idx != -1 {
		t.Errorf("expected -1, got %d", idx)
	}
}

func TestSkipRulesReported(t *testing.T) {
	const bundle = `{
      "source":"test","engine_version":3,
      "manifests":[{"id":"claude","rules":[
        {"id":"viewer","state":"unknown","skip_state_update":true,"contains":["showing detailed transcript"]}
      ]}]
    }`
	eng, err := loadEngine(bundle)
	if err != nil {
		t.Fatal(err)
	}
	d := eng.detect("claude", "showing detailed transcript", "", "")
	if !d.SkipStateUpdate || d.State != StateUnknown || d.HasEvidence() {
		t.Errorf("got %+v", d)
	}
}

func TestEngineLoadErrors(t *testing.T) {
	for name, bundle := range map[string]string{
		"empty":        `{}`,
		"empty object": `{"source":"t","engine_version":3,"manifests":[]}`,
		"no rules":     `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[]}]}`,
		"duplicate":    `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r"}]},{"id":"X","rules":[{"id":"r"}]}]}`,
		"bad region":   `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r","region":"middle_lines(3)"}]}]}`,
		"bad state":    `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r","state":"sleeping"}]}]}`,
		"bad regex":    `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r","regex":["(unclosed"]}]}]}`,
		"bad field":    `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r","bogus":1}]}]}`,
		"not json":     `definitely not json`,
	} {
		if _, err := loadEngine(bundle); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// Aliases keep the first manifest on collision.
	eng, err := loadEngine(`{"source":"t","engine_version":3,"manifests":[
      {"id":"a","rules":[{"id":"r"}]},{"id":"b","aliases":["a"],"rules":[{"id":"r"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if eng.manifest("a").id != "a" {
		t.Errorf("alias collision should keep first, got %q", eng.manifest("a").id)
	}
}

func TestRuleDefaults(t *testing.T) {
	// Missing region defaults to whole_recent; missing state to Unknown.
	eng, err := loadEngine(`{"source":"t","engine_version":3,"manifests":[
      {"id":"z","rules":[{"id":"r","contains":["needle"]}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	d := eng.detect("z", "a needle here\n", "", "")
	if d.State != StateUnknown || d.MatchedRule != "r" {
		t.Errorf("got %+v", d)
	}
	// visible flags only light up for their own state.
	eng2, err := loadEngine(`{"source":"t","engine_version":3,"manifests":[
      {"id":"z","rules":[{"id":"r","state":"idle","visible_blocker":true,"visible_idle":true,"contains":["needle"]}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	d2 := eng2.detect("z", "a needle here\n", "", "")
	if !d2.VisibleIdle || d2.VisibleBlocker || d2.HasEvidence() != true {
		t.Errorf("got %+v", d2)
	}
}

func TestNormalizeApp(t *testing.T) {
	table := map[string]string{
		"":                        "",
		"  Claude-Code  ":         "claude-code",
		"/usr/local/bin/opencode": "opencode",
		"codex.js":                "codex",
		"agent.EXE":               "agent",
		"run.mjs":                 "run",
		"trail/":                  "trail",
		"a/b/cursor-agent":        "cursor-agent",
		"UPPER.CMD":               "upper",
		"keep.batx":               "keep.batx",
		"C:\\tools\\gemini":       `c:\tools\gemini`, // backslash is not a separator
		"herdr:opencode":          "herdr:opencode",
	}
	for in, want := range table {
		if got := normalizeApp(in); got != want {
			t.Errorf("normalizeApp(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranslatedSpinnerPatternsEndToEnd(t *testing.T) {
	// These rules use Rust-only escapes (\uHHHH, \p{Alphabetic}) that the
	// engine translates for RE2; detect through them to prove the
	// translation preserves matching behavior.
	cases := []struct {
		app    string
		screen string
	}{
		{"agy", "doing things\n⠋ Working on it\n"},
		{"kimi", "⠙ thinking...\n"},
		{"qodercli", "⠋ Loading context\n"},
		{"droid", "⠋ working\nesc to stop\n"},
	}
	for _, tc := range cases {
		d := Detect(tc.app, tc.screen, "")
		if d.State != StateWorking || !d.VisibleWorking {
			t.Errorf("Detect(%q, %q) = %+v, want working", tc.app, tc.screen, d)
		}
	}
}

func TestUnknownManifestHasNoRules(t *testing.T) {
	d := engine().detect("no-such-agent", "anything at all", "", "")
	if d != unknownDetected("") {
		t.Errorf("got %+v", d)
	}
}

func TestHasEvidence(t *testing.T) {
	if (Detected{}).HasEvidence() {
		t.Error("zero Detected should have no evidence")
	}
	if !(Detected{VisibleIdle: true}).HasEvidence() ||
		!(Detected{VisibleBlocker: true}).HasEvidence() ||
		!(Detected{VisibleWorking: true}).HasEvidence() {
		t.Error("visible flags should count as evidence")
	}
}

func TestAntigravityIdlePromptAndWorkingPriority(t *testing.T) {
	idleViewport := strings.Join([]string{
		"  Done with the change.",
		"────────────────────────────────────────────────────────────────────────────────",
		">",
		"────────────────────────────────────────────────────────────────────────────────",
		"? for shortcuts                                    auto · Gemini 4 Hydrogen · medium",
	}, "\n")
	d := Detect("antigravity", idleViewport, "")
	if d.State != StateIdle || !d.VisibleIdle {
		t.Fatalf("idle antigravity viewport: got %+v, want StateIdle + VisibleIdle", d)
	}

	// While esc to cancel is visible, working rule wins over idle_prompt.
	workingViewport := strings.Join([]string{
		"⠋ Thinking...",
		"────────────────────────────────────────────────────────────────────────────────",
		">",
		"──────────────── esc to cancel ────────────────",
	}, "\n")
	dw := Detect("antigravity", workingViewport, "")
	if dw.State != StateWorking || !dw.VisibleWorking {
		t.Fatalf("working antigravity viewport: got %+v, want StateWorking + VisibleWorking", dw)
	}
}
