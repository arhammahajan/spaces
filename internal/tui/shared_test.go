package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

type fakeSharedBackend struct {
	fakeBackend
	snap        model.SharedSnapshot
	loadErr     error
	loadDirs    []string
	openedFiles []string
	openErr     error
}

func (f *fakeSharedBackend) LoadShared(_ context.Context, dir string) (model.SharedSnapshot, error) {
	f.loadDirs = append(f.loadDirs, dir)
	if f.loadErr != nil {
		return model.SharedSnapshot{}, f.loadErr
	}
	return f.snap, nil
}

func (f *fakeSharedBackend) OpenSharedFile(_ context.Context, path string) error {
	if f.openErr != nil {
		return f.openErr
	}
	f.openedFiles = append(f.openedFiles, path)
	return nil
}

func sampleSharedSnapshot() model.SharedSnapshot {
	return model.SharedSnapshot{
		RootDir:    "/home/u/dev/dots/harnesses/shared",
		SkillsDir:  "/home/u/dev/dots/harnesses/shared/skills",
		RootExists: true,
		MCPs: []model.SharedMCP{
			{
				Name:        "agent-browser",
				Transport:   "stdio",
				Command:     "/home/u/.local/bin/agent-browser",
				Args:        []string{"mcp", "--tools", "core,debug"},
				Description: "agent-browser controls for the existing shared Helium browser.",
				SourcePath:  "/home/u/dev/dots/harnesses/pi/mcp.json",
				SourceKind:  "harnesses",
				Harnesses:   []string{"claude", "codex", "pi"},
				RawJSON:     `{"command": "/home/u/.local/bin/agent-browser"}`,
			},
		},
		Skills: []model.SharedSkill{
			{
				Name:                   "bro",
				DirName:                "bro",
				Description:            "Restate the last message in plain human language, with no jargon.",
				Path:                   "/home/u/dev/dots/harnesses/shared/skills/bro/SKILL.md",
				DirPath:                "/home/u/dev/dots/harnesses/shared/skills/bro",
				DisableModelInvocation: true,
				Body:                   "# Bro\n\nRestate your last message clearly.\n\n```sh\necho hi\n```",
			},
			{
				Name:          "ketchup",
				DirName:       "ketchup",
				Description:   "Catch the user up on what changed in this chat since their last message.",
				Path:          "/home/u/dev/dots/harnesses/shared/skills/ketchup/SKILL.md",
				DirPath:       "/home/u/dev/dots/harnesses/shared/skills/ketchup",
				HasReferences: true,
				ExtraFiles:    2,
				Body:          "# Ketchup\n\nCatch up clearly.",
			},
		},
	}
}

func newTestSharedModel(t *testing.T, fb *fakeSharedBackend, opts Options) Model {
	t.Helper()
	opts.InitialMode = ModeShared
	if opts.HomeDir == "" {
		opts.HomeDir = "/home/u"
	}
	m := New(fb, opts)
	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("ModeShared Init() returned nil cmd")
	}
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(Model)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 110, Height: 28})
	return next.(Model)
}

func TestSharedRendersSectionsAndCounts(t *testing.T) {
	fb := &fakeSharedBackend{snap: sampleSharedSnapshot()}
	m := newTestSharedModel(t, fb, Options{})

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Shared MCP & Skills",
		"1 mcp",
		"2 skills",
		"MCP SERVERS",
		"agent-browser",
		"SKILLS",
		"bro",
		"ketchup",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}

	// Verify no rendered line exceeds viewport width.
	for i, ln := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(ln); w > 110 {
			t.Errorf("line %d width = %d > 110: %q", i, w, ansi.Strip(ln))
		}
	}
}

func TestSharedFilterTabsAndFuzzySearch(t *testing.T) {
	fb := &fakeSharedBackend{snap: sampleSharedSnapshot()}
	m := newTestSharedModel(t, fb, Options{})

	// Tab cycles all -> mcp -> skills -> all.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if m.sharedFilter != model.SharedFilterMCP {
		t.Fatalf("after Tab filter = %v, want mcp", m.sharedFilter)
	}
	if len(m.sharedItems()) != 1 || !m.sharedItems()[0].IsMCP {
		t.Fatalf("mcp filter items = %+v", m.sharedItems())
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if m.sharedFilter != model.SharedFilterSkills {
		t.Fatalf("after 2nd Tab filter = %v, want skills", m.sharedFilter)
	}
	if len(m.sharedItems()) != 2 {
		t.Fatalf("skills filter count = %d, want 2", len(m.sharedItems()))
	}

	// Shift-Tab goes back to mcp; Esc clears filter without quitting.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.Quitting() || m.sharedFilter != model.SharedFilterAll {
		t.Fatalf("Esc with filter should reset, quitting=%v filter=%v", m.Quitting(), m.sharedFilter)
	}

	// Search-first typing filters immediately.
	for _, r := range "ketch" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	items := m.sharedItems()
	if len(items) != 1 || items[0].Name() != "ketchup" {
		t.Fatalf("fuzzy search items = %+v, want [ketchup]", items)
	}

	// Esc clears query first, second Esc quits.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.Quitting() || m.query != "" {
		t.Fatalf("1st Esc should clear query; quitting=%v query=%q", m.Quitting(), m.query)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if !m.Quitting() {
		t.Fatalf("2nd Esc should quit")
	}
}

func TestSharedInspectorAndOpenEditor(t *testing.T) {
	fb := &fakeSharedBackend{snap: sampleSharedSnapshot()}
	m := newTestSharedModel(t, fb, Options{})

	// Move down to "bro" skill (index 1) and press Enter to inspect.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if !m.sharedInspecting {
		t.Fatalf("Enter should open inspector")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"bro", "SKILL.md", "Restate your last message clearly."} {
		if !strings.Contains(view, want) {
			t.Errorf("inspector view missing %q:\n%s", want, view)
		}
	}

	// Scroll down & back up.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(Model)
	if m.sharedDetailScroll != 1 {
		t.Errorf("scroll = %d, want 1", m.sharedDetailScroll)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.sharedInspecting {
		t.Fatalf("Esc in inspector should return to list")
	}

	// Ctrl-o opens selected file in editor and quits.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(Model)
	if !m.Quitting() {
		t.Fatalf("Ctrl-o should quit after opening file")
	}
	if len(fb.openedFiles) != 1 || fb.openedFiles[0] != "/home/u/dev/dots/harnesses/shared/skills/bro/SKILL.md" {
		t.Fatalf("openedFiles = %v", fb.openedFiles)
	}
}

func TestSharedRefreshReloads(t *testing.T) {
	fb := &fakeSharedBackend{snap: sampleSharedSnapshot()}
	m := newTestSharedModel(t, fb, Options{SharedDir: "/custom/shared"})
	if len(fb.loadDirs) != 1 || fb.loadDirs[0] != "/custom/shared" {
		t.Fatalf("initial loadDirs = %v", fb.loadDirs)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = next.(Model)
	if cmd == nil {
		t.Fatalf("Ctrl-r should return reload cmd")
	}
	next, _ = m.Update(cmd())
	_ = next.(Model)
	if len(fb.loadDirs) != 2 {
		t.Fatalf("expected 2 loads after Ctrl-r, got %d", len(fb.loadDirs))
	}
}
