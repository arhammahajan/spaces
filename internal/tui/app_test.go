package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arhammahajan/spaces/internal/model"
)

type fakeBackend struct {
	spaces    []model.Space
	hook      *model.HookDetail
	harness   []model.HarnessEntry
	hookErr   error
	spacesErr error

	opened     []string
	focused    []string
	created    [][2]string
	renamed    [][2]string
	killed     []string
	launched   []string
	createErr  error
	launchErr  error
	renameErr  error
	killErr    error
	openErr    error
	harnessErr error
}

func (f *fakeBackend) LoadSpaces(ctx context.Context) ([]model.Space, error) {
	if f.spacesErr != nil {
		return nil, f.spacesErr
	}
	return f.spaces, nil
}
func (f *fakeBackend) LoadHookDetail(ctx context.Context, session string, paneNum int) (*model.HookDetail, error) {
	if f.hookErr != nil {
		return nil, f.hookErr
	}
	return f.hook, nil
}
func (f *fakeBackend) LoadHarnesses(ctx context.Context) ([]model.HarnessEntry, error) {
	if f.harnessErr != nil {
		return nil, f.harnessErr
	}
	return f.harness, nil
}
func (f *fakeBackend) OpenSpace(ctx context.Context, name string) error {
	if f.openErr != nil {
		return f.openErr
	}
	f.opened = append(f.opened, name)
	return nil
}
func (f *fakeBackend) FocusPane(ctx context.Context, session, windowID, paneID string) error {
	f.focused = append(f.focused, session+"/"+windowID+"/"+paneID)
	return nil
}
func (f *fakeBackend) CreateSpace(ctx context.Context, name, cwd string) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, [2]string{name, cwd})
	return nil
}
func (f *fakeBackend) RenameSpace(ctx context.Context, oldName, newName string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	f.renamed = append(f.renamed, [2]string{oldName, newName})
	return nil
}
func (f *fakeBackend) KillSpace(ctx context.Context, name string) error {
	if f.killErr != nil {
		return f.killErr
	}
	f.killed = append(f.killed, name)
	return nil
}
func (f *fakeBackend) LaunchAgent(ctx context.Context, session, cwd, agent, profile string) error {
	if f.launchErr != nil {
		return f.launchErr
	}
	f.launched = append(f.launched, agent+"/"+profile+"@"+session)
	return nil
}

func testSpaces() []model.Space {
	mk := func(name string, cur bool, panes []model.Pane) model.Space {
		var c model.Counts
		for _, p := range panes {
			c.Add(p.Status)
		}
		return model.Space{Name: name, IsCurrent: cur, Panes: panes, Counts: c, Status: c.Rollup(), CWD: "/home/u/" + name}
	}
	return []model.Space{
		mk("dots", true, []model.Pane{
			{ID: "%1", NumericID: 1, WindowID: "@1", WindowIndex: 0, WindowName: "edit", Title: "opencode", Command: "opencode", App: "opencode", Status: model.StatusWorking, Text: "editing auth", CWD: "/home/u/dots", AgeMs: 12000},
			{ID: "%2", NumericID: 2, WindowID: "@1", WindowIndex: 0, WindowName: "edit", Title: "claude", Command: "claude", App: "claude", Status: model.StatusNeedsYou, Text: "approve migration", CWD: "/home/u/dots", AgeMs: 90000, Hook: &model.HookSummary{Agent: "claude", App: "claude", State: "blocked", Kind: "permission", Event: "pre_tool", TsMs: 9000, Text: "approve", HasDetail: true}},
		}),
		mk("server", false, []model.Pane{
			{ID: "%3", NumericID: 3, WindowID: "@1", WindowIndex: 0, WindowName: "logs", Title: "zsh", Command: "zsh", App: "shell", Status: model.StatusIdle, CWD: "/srv/app"},
		}),
	}
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func keyType(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func update(m Model, msg tea.Msg) Model {
	u, _ := m.Update(msg)
	return u.(Model)
}

func TestNavigateMoveAndJump(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m.width, m.height = 100, 30
	// initial selection row 0 (space dots)
	if got := m.selectedRow(); got == nil || got.Kind != RowSpace {
		t.Fatalf("expected space row, got %+v", got)
	}
	m = update(m, keyType(tea.KeyDown))
	if got := m.selectedRow(); got == nil || got.Kind != RowPane {
		t.Fatalf("expected pane row after down, got %+v", got)
	}
	m = update(m, keyRunes("j"))
	m = update(m, keyRunes("k"))
	// jump spaces with l
	m.selected = 0
	m = update(m, keyRunes("l"))
	if got := m.selectedRow(); got == nil {
		t.Fatal("nil row after jump")
	}
	// 1..9 jumps to space N: press 2 -> server space
	m.selected = 0
	m = update(m, keyRunes("2"))
	r := m.selectedRow()
	if r == nil || r.Kind != RowSpace || m.spaces[r.SpaceIdx].Name != "server" {
		t.Fatalf("expected server space, got %+v", r)
	}
}

func TestEnterOpensSpaceAndQuits(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m.width, m.height = 100, 30
	// select server space
	m.selected = 0
	rows := m.visibleRows()
	for i, r := range rows {
		if r.Kind == RowSpace && m.spaces[r.SpaceIdx].Name == "server" {
			m.selected = i
		}
	}
	u, cmd := m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if len(fb.opened) != 1 || fb.opened[0] != "server" {
		t.Fatalf("expected OpenSpace server, got %v", fb.opened)
	}
	if !m.quitting || cmd == nil {
		t.Fatal("expected quitting with quit cmd")
	}
}

func TestEnterFocusesPane(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m.width, m.height = 100, 30
	// select first pane
	m.selected = 1
	m.activateSelected()
	if len(fb.focused) != 1 {
		t.Fatalf("expected focus, got %v", fb.focused)
	}
	if !strings.Contains(fb.focused[0], "dots") {
		t.Fatalf("unexpected focus %v", fb.focused)
	}
}

func TestAttentionFilters(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m = update(m, keyRunes("b"))
	if m.filter != model.FilterNeeds {
		t.Fatalf("expected needs filter, got %v", m.filter)
	}
	rows := m.visibleRows()
	if len(rows) != 2 { // space dots + blocked pane
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	m = update(m, keyRunes("b")) // toggles back to all
	if m.filter != model.FilterAll {
		t.Fatalf("expected all, got %v", m.filter)
	}
	m = update(m, keyRunes("w"))
	if m.filter != model.FilterWorking {
		t.Fatalf("expected working, got %v", m.filter)
	}
	m = update(m, keyRunes("d"))
	if m.filter != model.FilterDone {
		t.Fatalf("expected done, got %v", m.filter)
	}
	m = update(m, keyRunes("i"))
	if m.filter != model.FilterIdle {
		t.Fatalf("expected idle, got %v", m.filter)
	}
	m = update(m, keyRunes("a"))
	if m.filter != model.FilterAll {
		t.Fatalf("expected all, got %v", m.filter)
	}
}

func TestCollapseToggle(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	before := len(m.visibleRows())
	m.selected = 0
	// Bubbletea v1 delivers Spacebar as tea.KeySpace (not tea.KeyRunes).
	m = update(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	after := len(m.visibleRows())
	if after >= before {
		t.Fatalf("expected fewer rows after KeySpace collapse: %d -> %d", before, after)
	}
	m = update(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if len(m.visibleRows()) != before {
		t.Fatal("expected expand back on KeySpace")
	}
	// Also keep rune-based space working.
	m = update(m, keyRunes(" "))
	if len(m.visibleRows()) >= before {
		t.Fatalf("expected fewer rows after rune space collapse: %d -> %d", before, len(m.visibleRows()))
	}
	m = update(m, keyRunes(" "))
	if len(m.visibleRows()) != before {
		t.Fatal("expected expand back on rune space")
	}
}

func TestSearchLiveFilteringAndEsc(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m = update(m, keyRunes("/"))
	if m.mode != ModeSearch {
		t.Fatal("expected search mode")
	}
	for _, ch := range "serv" {
		m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	rows := m.visibleRows()
	if len(rows) == 0 {
		t.Fatal("expected rows for serv")
	}
	found := false
	for _, r := range rows {
		if r.Kind == RowSpace && m.spaces[r.SpaceIdx].Name == "server" {
			found = true
		}
	}
	if !found {
		t.Fatal("server should match serv")
	}
	// Esc clears query, returns to navigate
	m = update(m, keyType(tea.KeyEsc))
	if m.query != "" || m.mode != ModeNavigate {
		t.Fatalf("expected cleared navigate, got %q %v", m.query, m.mode)
	}
	// Second Esc quits
	u, _ := m.Update(keyType(tea.KeyEsc))
	if !u.(Model).quitting {
		t.Fatal("expected quit on second esc")
	}
}

func TestSearchCreateRow(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m.mode = ModeSearch
	m.query = "brandnewspace"
	m.cursor = len(m.query)
	rows := m.visibleRows()
	found := false
	for _, r := range rows {
		if r.Kind == RowCreate && r.CreateName == "brandnewspace" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected create row")
	}
	// Enter on create row opens new space dialog
	for i, r := range rows {
		if r.Kind == RowCreate {
			m.selected = i
		}
	}
	m.activateSelected()
	if m.mode != ModeNewSpace {
		t.Fatalf("expected new space mode, got %v", m.mode)
	}
}

func TestNewSpaceDialogSubmitAndCancel(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{HomeDir: "/home/u"})
	m.spaces = testSpaces()
	m.openNew("")
	if m.mode != ModeNewSpace {
		t.Fatal("expected new space")
	}
	if m.newName == "" || m.newCWD == "" {
		t.Fatalf("expected prefill, got %q %q", m.newName, m.newCWD)
	}
	// Empty name -> error
	m.newName = "  "
	m.newNameCur = 0
	m = update(m, keyType(tea.KeyEnter))
	if m.mode != ModeNewSpace || m.newError == "" {
		t.Fatal("expected validation error")
	}
	// Tab switches field
	m.newName = "fresh"
	m.newNameCur = 5
	m = update(m, keyType(tea.KeyTab))
	if m.newField != 1 {
		t.Fatal("expected cwd field")
	}
	m = update(m, keyType(tea.KeyTab))
	// Submit
	u, cmd := m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if len(fb.created) != 1 || fb.created[0][0] != "fresh" {
		t.Fatalf("expected create fresh, got %v err=%q cmd=%v", fb.created, m.newError, cmd != nil)
	}
	if !m.quitting {
		t.Fatal("expected quit after create")
	}
	// Cancel path
	m2 := New(fb, Options{})
	m2.spaces = testSpaces()
	m2.openNew("")
	m2 = update(m2, keyType(tea.KeyEsc))
	if m2.mode != ModeNavigate {
		t.Fatal("expected navigate after cancel")
	}
}

func TestRenameDialog(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	// select dots space (current) - rename allowed; requestRename doesn't check current
	// to mirror spec loosely, but backend call is what matters.
	m.selected = 0
	m.requestRename()
	if m.mode != ModeRename {
		t.Fatal("expected rename mode")
	}
	if m.renameText != "dots" {
		t.Fatalf("expected prefill dots, got %q", m.renameText)
	}
	// type X
	m = update(m, keyRunes("X"))
	if m.renameText != "dotsX" {
		t.Fatalf("got %q", m.renameText)
	}
	u, _ := m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if len(fb.renamed) != 1 || fb.renamed[0][0] != "dots" || fb.renamed[0][1] != "dotsX" {
		t.Fatalf("expected rename, got %v", fb.renamed)
	}
	if !m.quitting {
		t.Fatal("expected quit after rename")
	}
	// cancel
	m2 := New(fb, Options{})
	m2.spaces = testSpaces()
	m2.selected = 0
	m2.requestRename()
	m2 = update(m2, keyType(tea.KeyEsc))
	if m2.mode != ModeNavigate {
		t.Fatal("expected cancel")
	}
}

type fakeWindowRenameBackend struct {
	*fakeBackend
	renamedWindows [][2]string
}

func (f *fakeWindowRenameBackend) RenameWindow(_ context.Context, windowID, newName string) error {
	f.renamedWindows = append(f.renamedWindows, [2]string{windowID, newName})
	return nil
}

func TestStandaloneRenameWindowPopup(t *testing.T) {
	spaces := testSpaces()
	spaces[0].Windows = []model.Window{
		{ID: "@1", Index: 1, Name: "claude", Active: true},
		{ID: "@2", Index: 2, Name: "shell"},
	}
	fb := &fakeWindowRenameBackend{fakeBackend: &fakeBackend{spaces: spaces}}
	m := New(fb, Options{OpenRenameWindow: true})
	if m.mode != ModeRename || !m.renameWindow || !m.renameStandalone {
		t.Fatalf("unexpected initial state: mode=%v win=%v standalone=%v", m.mode, m.renameWindow, m.renameStandalone)
	}
	m = update(m, spacesMsg{spaces: spaces})
	if m.renameWindowID != "@1" || m.renameText != "claude" {
		t.Fatalf("prefill = (%q, %q), want (@1, claude)", m.renameWindowID, m.renameText)
	}
	if v := m.View(); !strings.Contains(v, "Rename tab") {
		t.Fatalf("expected Rename tab title in view:\n%s", v)
	}
	m.width = 80
	m.height = 1
	if v := m.View(); !strings.Contains(v, "Rename tab: claude_") || strings.Contains(v, "\n") {
		t.Fatalf("expected single-line bottom bar view, got:\n%q", v)
	}
	m = update(m, keyRunes("2"))
	m = update(m, keyType(tea.KeyEnter))
	if len(fb.renamedWindows) != 1 || fb.renamedWindows[0] != [2]string{"@1", "claude2"} {
		t.Fatalf("renamedWindows = %v", fb.renamedWindows)
	}
	if !m.quitting {
		t.Fatal("expected quit after standalone window rename")
	}

	// Esc quits standalone rename popup directly instead of opening navigator.
	mEsc := New(fb, Options{OpenRenameWindow: true})
	mEsc = update(mEsc, spacesMsg{spaces: spaces})
	mEsc = update(mEsc, keyType(tea.KeyEsc))
	if !mEsc.quitting {
		t.Fatal("expected Esc to quit standalone rename popup")
	}
}

func TestConfirmKillDialog(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	// select server
	for i, r := range m.visibleRows() {
		if r.Kind == RowSpace && m.spaces[r.SpaceIdx].Name == "server" {
			m.selected = i
		}
	}
	m.requestKill()
	if m.mode != ModeConfirmKill {
		t.Fatal("expected confirm")
	}
	if m.confirmSpace != "server" || m.confirmPanes != 1 {
		t.Fatalf("bad confirm %+v", m)
	}
	// n cancels
	m = update(m, keyRunes("n"))
	if m.mode != ModeNavigate {
		t.Fatal("expected cancel")
	}
	// reopen, confirm with y
	m.requestKill()
	m = update(m, keyRunes("y"))
	if len(fb.killed) != 1 || fb.killed[0] != "server" {
		t.Fatalf("expected kill, got %v", fb.killed)
	}
	if len(m.spaces) != 1 {
		t.Fatal("expected space removed locally")
	}
	// current space cannot be killed
	m3 := New(fb, Options{})
	m3.spaces = testSpaces()
	m3.selected = 0 // dots is current
	m3.requestKill()
	if m3.mode == ModeConfirmKill {
		t.Fatal("current space should not offer kill")
	}
	if m3.toast == "" {
		t.Fatal("expected toast")
	}
}

func TestHookDetailOpenScrollClose(t *testing.T) {
	detail := &model.HookDetail{
		HookSummary: model.HookSummary{Agent: "claude", App: "claude", State: "blocked", Kind: "permission", Text: "approve", Model: "m1", SessionID: "s1", CWD: "/tmp", HasDetail: true},
		Prompt:      "fix it",
		Tool:        &model.HookTool{Name: "Bash", Input: "cargo test", Output: "ok"},
		Reply:       "done",
		Children:    []model.HookChild{{ID: "a1", Kind: "Explore", State: "working", Text: "find"}},
	}
	fb := &fakeBackend{spaces: testSpaces(), hook: detail}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m.width, m.height = 100, 30
	// select pane with hook (dots pane 2)
	rows := m.visibleRows()
	for i, r := range rows {
		if r.Kind == RowPane && m.spaces[r.SpaceIdx].Name == "dots" && m.spaces[r.SpaceIdx].Panes[r.PaneIdx].NumericID == 2 {
			m.selected = i
		}
	}
	u, cmd := m.Update(keyRunes("H"))
	m = u.(Model)
	if m.mode != ModeHookDetail {
		t.Fatalf("expected hook detail, toast=%q", m.toast)
	}
	if cmd == nil {
		t.Fatal("expected load cmd when has_detail")
	}
	// deliver detail
	u, _ = m.Update(hookDetailMsg{session: "dots", paneNum: 2, detail: detail})
	m = u.(Model)
	if m.hookDetail == nil || m.hookDetail.Prompt != "fix it" {
		t.Fatal("expected detail cached")
	}
	m = update(m, keyType(tea.KeyDown))
	if m.hookScroll != 1 {
		t.Fatalf("expected scroll 1, got %d", m.hookScroll)
	}
	m = update(m, keyType(tea.KeyPgDown))
	m = update(m, keyType(tea.KeyPgUp))
	m = update(m, keyType(tea.KeyUp))
	m = update(m, keyRunes("H"))
	if m.mode != ModeNavigate {
		t.Fatal("expected close on H")
	}
}

func TestHookDetailRequiresPane(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m.selected = 0 // space row
	m = update(m, keyRunes("H"))
	if m.mode == ModeHookDetail {
		t.Fatal("should not open on space row")
	}
	if m.toast == "" {
		t.Fatal("expected toast")
	}
}

func TestHelpToggle(t *testing.T) {
	fb := &fakeBackend{}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m = update(m, keyRunes("?"))
	if m.mode != ModeHelp {
		t.Fatal("expected help")
	}
	m = update(m, keyType(tea.KeyEsc))
	if m.mode != ModeNavigate {
		t.Fatal("expected navigate")
	}
}

func TestRefreshKey(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	u, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	_ = u
	if cmd == nil {
		t.Fatal("expected refresh cmd")
	}
	msg := cmd()
	if _, ok := msg.(spacesMsg); !ok {
		t.Fatalf("expected spacesMsg, got %T", msg)
	}
}

func TestAgentsListFilterAndLaunch(t *testing.T) {
	fb := &fakeBackend{
		spaces: testSpaces(),
		harness: []model.HarnessEntry{
			{Name: "claude", Description: "Claude Code"},
			{Name: "opencode", Description: "OpenCode server"},
		},
	}
	m := New(fb, Options{InitialMode: ModeAgents})
	if !m.agentsLoading {
		t.Fatal("expected loading")
	}
	u, _ := m.Update(harnessesMsg{entries: fb.harness})
	m = u.(Model)
	if len(m.harnesses) != 2 {
		t.Fatal("expected harnesses")
	}
	// filter
	m = update(m, keyRunes("c"))
	m = update(m, keyRunes("l"))
	m = update(m, keyRunes("a"))
	if len(m.filteredHarnessIdx()) != 1 {
		t.Fatalf("expected 1 match, got %v", m.filteredHarnessIdx())
	}
	// clear with backspace x3
	m = update(m, keyType(tea.KeyBackspace))
	m = update(m, keyType(tea.KeyBackspace))
	m = update(m, keyType(tea.KeyBackspace))
	if len(m.filteredHarnessIdx()) != 2 {
		t.Fatal("expected all after clear")
	}
	// move with ctrl-n/ctrl-p
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlN})
	if m.harnessCursor != 1 {
		t.Fatalf("expected cursor 1, got %d", m.harnessCursor)
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.harnessCursor != 0 {
		t.Fatalf("expected cursor 0, got %d", m.harnessCursor)
	}
	// Enter launches directly (no profiles)
	u, _ = m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if len(fb.launched) != 1 || !strings.HasPrefix(fb.launched[0], "claude/") {
		t.Fatalf("expected launch claude, got %v", fb.launched)
	}
	if !m.quitting {
		t.Fatal("expected quit after launch")
	}
}

func TestAgentsProfileDrilldown(t *testing.T) {
	fb := &fakeBackend{
		spaces: testSpaces(),
		harness: []model.HarnessEntry{
			{Name: "codex", Description: "Codex", Profiles: []model.HarnessProfile{
				{Name: "router", DisplayName: "Codex Router", Source: "t3"},
				{Name: "local", DisplayName: "Local", Source: "user"},
			}},
		},
	}
	m := New(fb, Options{InitialMode: ModeAgents})
	u, _ := m.Update(harnessesMsg{entries: fb.harness})
	m = u.(Model)
	// Enter drills into profiles
	u, _ = m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if m.profileOf == nil {
		t.Fatal("expected profile drilldown")
	}
	rows := m.profileRows()
	if len(rows) != 3 { // Default + 2
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// Move to first profile and launch
	m.harnessCursor = 1
	u, _ = m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if len(fb.launched) != 1 || !strings.Contains(fb.launched[0], "codex/router") {
		t.Fatalf("expected codex/router launch, got %v", fb.launched)
	}
}

func TestAgentsBackAndRefresh(t *testing.T) {
	fb := &fakeBackend{
		spaces:  testSpaces(),
		harness: []model.HarnessEntry{{Name: "a", Description: "A", Profiles: []model.HarnessProfile{{Name: "p"}}}},
	}
	m := New(fb, Options{InitialMode: ModeAgents})
	u, _ := m.Update(harnessesMsg{entries: fb.harness})
	m = u.(Model)
	u, _ = m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if m.profileOf == nil {
		t.Fatal("expected profiles view")
	}
	// Esc goes back
	u, _ = m.Update(keyType(tea.KeyEsc))
	m = u.(Model)
	if m.profileOf != nil {
		t.Fatal("expected back to list")
	}
	// Ctrl-r refreshes
	u, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = u.(Model)
	if cmd == nil {
		t.Fatal("expected refresh cmd")
	}
	msg := cmd()
	if _, ok := msg.(harnessesMsg); !ok {
		t.Fatalf("expected harnessesMsg, got %T", msg)
	}
	// Esc quits from list
	u, _ = m.Update(keyType(tea.KeyEsc))
	if !u.(Model).quitting {
		t.Fatal("expected quit")
	}
}

func TestAgentsLaunchErrorToast(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces(), harness: []model.HarnessEntry{{Name: "x", Description: "X"}}, launchErr: errors.New("boom")}
	m := New(fb, Options{InitialMode: ModeAgents})
	u, _ := m.Update(harnessesMsg{entries: fb.harness})
	m = u.(Model)
	u, _ = m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if m.quitting {
		t.Fatal("should not quit on error")
	}
	if !strings.Contains(m.toast, "boom") {
		t.Fatalf("expected toast boom, got %q", m.toast)
	}
}

func TestMouseWheelAndClick(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	m.width, m.height = 100, 30
	m.selected = 0
	u, _ := m.Update(tea.MouseMsg{X: 5, Y: 4, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = u.(Model)
	if m.selected != 1 {
		t.Fatalf("expected scroll down to 1, got %d", m.selected)
	}
	u, _ = m.Update(tea.MouseMsg{X: 5, Y: 4, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = u.(Model)
	if m.selected != 0 {
		t.Fatalf("expected scroll up to 0, got %d", m.selected)
	}
	// Left click selects row 1 (y = layout.topY + 1)
	layout := m.navigatorLayout(m.width, m.height)
	row1Y := layout.topY + 1
	u, _ = m.Update(tea.MouseMsg{X: 5, Y: row1Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = u.(Model)
	if m.selected != 1 {
		t.Fatalf("expected click select 1, got %d", m.selected)
	}
	// Double-click (same row quickly) opens
	m.lastClickRow = 1
	m.lastClickAt = time.Now()
	u, _ = m.Update(tea.MouseMsg{X: 5, Y: row1Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = u.(Model)
	if len(fb.focused) != 1 {
		t.Fatalf("expected double-click focus, got %v", fb.focused)
	}
}

func TestTickReloadsInNavigatorNotAgents(t *testing.T) {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{})
	m.spaces = testSpaces()
	u, cmd := m.Update(tickMsg(time.Now()))
	_ = u
	if cmd == nil {
		t.Fatal("expected tick cmd in navigator")
	}
	m2 := New(fb, Options{InitialMode: ModeAgents})
	u2, cmd2 := m2.Update(tickMsg(time.Now()))
	_ = u2
	if cmd2 != nil {
		t.Fatal("expected no tick cmd in agents mode")
	}
}
