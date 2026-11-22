package agent

import (
	"context"
	"testing"

	"github.com/arhammahajan/spaces/internal/screen"
)

type fakeScreenReader struct {
	state screen.State
}

func (f fakeScreenReader) HasManifest() bool { return true }
func (f fakeScreenReader) Detect(viewport, oscTitle string) screen.Detected {
	return screen.Detected{
		State:          f.state,
		VisibleWorking: f.state == screen.StateWorking,
		MatchedRule:    "fake_rule",
	}
}

type fakeConvStore struct {
	records []ConversationRecord
}

func (f fakeConvStore) List(context.Context, ConversationListOptions) ([]ConversationRecord, error) {
	return f.records, nil
}
func (f fakeConvStore) Search(context.Context, string, ConversationListOptions) ([]ConversationRecord, error) {
	return f.records, nil
}

type fakeLogParser struct {
	entries []LogEntry
}

func (f fakeLogParser) ParseLog(context.Context, string) ([]LogEntry, error) {
	return f.entries, nil
}

func TestRegistryCustomPluginEndToEnd(t *testing.T) {
	reg := NewRegistry()
	custom := &BasePlugin{
		PluginSpec: Spec{
			ID:                      "acme-coder",
			Aliases:                 []string{"acme", "acme-cli"},
			DisplayApp:              "acme-coder",
			CLIDisplayApp:           "acme-code",
			ConversationID:          "acme-coder",
			ShortName:               "acme",
			Binary:                  "acme-bin",
			HarnessManaged:          true,
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
			TitlePrefixes:           []string{"ACME | "},
			WorktreeTitlePrefixes:   []string{"acme | ", "acme - "},
			BoilerplateTitles:       []string{"acme session"},
		},
		MCPTargets: []MCPConfigTarget{
			{RelPath: ".acme/mcp.json"},
		},
		CustomScreenReader: fakeScreenReader{state: screen.StateWorking},
		CustomTitleExtractor: TitleExtractorFunc(func(viewport string) string {
			return "Refactor billing"
		}),
		CustomConversationStore: fakeConvStore{
			records: []ConversationRecord{{Agent: "acme-coder", SessionID: "s1", Title: "Refactor billing"}},
		},
		CustomLogParser: fakeLogParser{
			entries: []LogEntry{{Role: "assistant", Text: "Done"}},
		},
		CustomSessionResolver: BaseSessionResolver{PositionalResume: true},
	}
	reg.Register(custom)

	// Alias resolution & normalization
	for _, input := range []string{"acme-coder", "acme", "acme-cli", "ACME", "/usr/local/bin/acme-bin-1.2.3", "herdr:acme", "acme-2.0.1"} {
		if !reg.IsKnown(input) {
			t.Errorf("IsKnown(%q) = false, want true", input)
		}
		if got := reg.CanonicalID(input); got != "acme-coder" {
			t.Errorf("CanonicalID(%q) = %q, want acme-coder", input, got)
		}
	}

	if got := reg.CLIDisplayApp("acme"); got != "acme-code" {
		t.Errorf("CLIDisplayApp(acme) = %q, want acme-code", got)
	}
	if got := reg.BinaryName("acme"); got != "acme-bin" {
		t.Errorf("BinaryName(acme) = %q, want acme-bin", got)
	}
	if got, ok := reg.ShortName("acme-cli"); !ok || got != "acme" {
		t.Errorf("ShortName(acme-cli) = (%q, %v), want (acme, true)", got, ok)
	}
	if !reg.IsOSCAuthority("acme") {
		t.Error("IsOSCAuthority(acme) = false, want true")
	}
	if !reg.IsHarnessManaged("acme") {
		t.Error("IsHarnessManaged(acme) = false, want true")
	}
	if !reg.IsVersionBannerName("acme") {
		t.Error("IsVersionBannerName(acme) = false, want true")
	}
	if !reg.HasScreenManifest("acme") {
		t.Error("HasScreenManifest(acme) = false, want true")
	}

	det := reg.DetectScreen("acme", "working...", "")
	if det.State != screen.StateWorking || det.Title != "Refactor billing" || det.MatchedRule != "fake_rule" {
		t.Errorf("DetectScreen = %+v, unexpected", det)
	}

	p, ok := reg.Resolve("acme")
	if !ok {
		t.Fatal("Resolve(acme) failed")
	}
	if sid := p.SessionResolver().ExtractSessionIDFromArgs([]string{"acme", "resume", "abc-123"}); sid != "abc-123" {
		t.Errorf("ExtractSessionIDFromArgs = %q, want abc-123", sid)
	}
	recs, err := p.ConversationStore().List(context.Background(), ConversationListOptions{})
	if err != nil || len(recs) != 1 || recs[0].SessionID != "s1" {
		t.Errorf("ConversationStore.List = (%+v, %v), want 1 record", recs, err)
	}
	entries, err := p.LogParser().ParseLog(context.Background(), "/tmp/log.jsonl")
	if err != nil || len(entries) != 1 || entries[0].Text != "Done" {
		t.Errorf("LogParser.ParseLog = (%+v, %v), want 1 entry", entries, err)
	}

	targets := reg.AllMCPConfigTargets()
	if len(targets) != 1 || targets[0].HarnessLabel != "acme-coder" || targets[0].RelPath != ".acme/mcp.json" {
		t.Errorf("AllMCPConfigTargets = %+v, unexpected", targets)
	}
}

func TestNormalizeCommand(t *testing.T) {
	cases := map[string]string{
		"claude":                   "claude",
		"/usr/bin/claude-code":     "claude-code",
		"herdr:opencode":           "opencode",
		"muse-bin-1.4.24-R9131.1":  "muse",
		"claude-2.1.76":            "claude",
		"codex-0.42.0.exe":         "codex",
		"  Kimi-CLI.JS  ":          "kimi-cli",
	}
	for in, want := range cases {
		if got := NormalizeCommand(in); got != want {
			t.Errorf("NormalizeCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractSessionIDFromArgs(t *testing.T) {
	if got := ExtractSessionIDFromArgs([]string{"claude", "--resume", "sess-1"}, false); got != "sess-1" {
		t.Errorf("got %q, want sess-1", got)
	}
	if got := ExtractSessionIDFromArgs([]string{"agy", "--conversation=conv-2"}, false); got != "conv-2" {
		t.Errorf("got %q, want conv-2", got)
	}
	if got := ExtractSessionIDFromArgs([]string{"codex", "resume", "cx-3"}, true); got != "cx-3" {
		t.Errorf("got %q, want cx-3", got)
	}
	if got := ExtractSessionIDFromArgs([]string{"codex", "resume", "--last"}, true); got != "" {
		t.Errorf("got %q, want empty for --last", got)
	}
	if got := ExtractSessionIDFromArgs([]string{"claude", "resume", "not-flag"}, false); got != "" {
		t.Errorf("got %q, want empty when positionalResume=false", got)
	}
}

func TestHarnessProfileFromArgv(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"harness", "profile", "work"}, "work"},
		{[]string{"/usr/bin/harness", "antigravity", "profile", "personal"}, "personal"},
		{[]string{"harness", "spaces", "__shim-profile", "antigravity/pro"}, "pro"},
		{[]string{"claude", "--resume", "abc"}, ""},
	}
	for _, tc := range cases {
		if got := HarnessProfileFromArgv(tc.argv); got != tc.want {
			t.Errorf("HarnessProfileFromArgv(%v) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}
