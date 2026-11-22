// Command tmux-spaces is the native Go + Bubbletea/Lipgloss tmux plugin and
// CLI for managing coding-agent spaces, attention states, screen-reader
// fallback, and agent launches.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/cli"
	"github.com/arhammahajan/spaces/internal/gitinfo"
	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/resurrect"
	"github.com/arhammahajan/spaces/internal/screen"
	"github.com/arhammahajan/spaces/internal/tmux"
	"github.com/arhammahajan/spaces/internal/tui"
	"github.com/arhammahajan/spaces/internal/worktree"
)

const (
	defaultTmuxTimeout    = 5 * time.Second
	screenCaptureInterval = 5 * time.Second
	maxCapturePanes       = 64
	defaultCaptureLines   = 200
)

func main() {
	client := tmux.DefaultClient()
	env := buildEnv(client)
	os.Exit(cli.Run(os.Args[1:], env))
}

func buildEnv(client *tmux.Client) cli.Env {
	backend := newTUIBackend(client)
	adapter := &tmuxCLIAdapter{client: client, backend: backend}

	env := cli.DefaultEnv(adapter)
	env.LoadSpaces = func() (tmux.ServerSnapshot, error) {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
		defer cancel()
		return backend.ResolveSnapshot(ctx)
	}
	env.RunPopup = func(e cli.Env, args []string) int {
		opts := parsePopupOptions(args)
		opts.HomeDir = homeDir()
		if err := tui.Run(backend, opts); err != nil {
			fmt.Fprintf(e.Stderr, "tmux-spaces popup: %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	env.RunAgents = func(e cli.Env, _ []string) int {
		opts := tui.Options{
			InitialMode: tui.ModeAgents,
			HomeDir:     homeDir(),
		}
		if err := tui.Run(backend, opts); err != nil {
			fmt.Fprintf(e.Stderr, "tmux-spaces agents: %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	env.RunInbox = func(e cli.Env, _ []string) int {
		opts := tui.Options{
			InitialMode: tui.ModeInbox,
			HomeDir:     homeDir(),
		}
		if err := tui.Run(backend, opts); err != nil {
			fmt.Fprintf(e.Stderr, "tmux-spaces inbox: %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	env.RunConversations = func(e cli.Env, args []string) int {
		opts := parseConversationsOptions(args)
		opts.HomeDir = homeDir()
		if err := tui.Run(backend, opts); err != nil {
			fmt.Fprintf(e.Stderr, "tmux-spaces conversations: %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	env.RunShared = func(e cli.Env, args []string) int {
		opts := parseSharedOptions(args)
		opts.HomeDir = homeDir()
		if err := tui.Run(backend, opts); err != nil {
			fmt.Fprintf(e.Stderr, "tmux-spaces shared: %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	env.RunTabs = func(e cli.Env, args []string) int {
		opts := parseTabsOptions(args)
		opts.HomeDir = homeDir()
		if err := tui.Run(backend, opts); err != nil {
			fmt.Fprintf(e.Stderr, "tmux-spaces tabs: %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	env.HookHealth = func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		snap, err := model.FetchHooks(ctx)
		if err != nil {
			return "", err
		}
		if !snap.OK {
			return "unhealthy", fmt.Errorf("hook store returned ok=false")
		}
		if snap.SpoolPending > 0 {
			return fmt.Sprintf("ok (%d spooled)", snap.SpoolPending), nil
		}
		return "ok", nil
	}
	env.RunResurrect = func(e cli.Env, args []string) int {
		return runResurrectCLI(client, backend.store, backend.selfPath, e, args)
	}
	env.RunWorktrees = func(e cli.Env, args []string) int {
		return runWorktreesCLI(client, backend, e, args)
	}
	env.MaybeAutoSave = func() {
		if !backend.store.ShouldAutoSave() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = saveSpacesToStore(ctx, client, backend.store, "")
	}
	env.RecordOutcome = func(rec cli.OutcomeRecord) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = model.RecordOutcome(ctx, model.OutcomeEntry{
			Session: rec.Session,
			Pane:    rec.Pane,
			App:     rec.App,
			Profile: rec.Profile,
			Outcome: rec.Outcome,
			Text:    rec.Text,
			CWD:     rec.CWD,
			TsMs:    rec.TsMs,
		})
	}
	return env
}

func parsePopupOptions(args []string) tui.Options {
	opts := tui.Options{
		InitialMode: tui.ModeNavigate,
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--new":
			opts.OpenNew = true
		case "--rename-window":
			opts.OpenRenameWindow = true
		case "--rename-space":
			opts.OpenRenameSpace = true
		case "--query":
			if i+1 < len(args) {
				i++
				opts.InitialQuery = args[i]
				opts.InitialMode = tui.ModeSearch
			}
		case "--target":
			if i+1 < len(args) {
				i++
				opts.Target = args[i]
			}
		}
	}
	return opts
}

func parseConversationsOptions(args []string) tui.Options {
	opts := tui.Options{
		InitialMode: tui.ModeConversations,
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--query" && i+1 < len(args) {
			i++
			opts.InitialQuery = args[i]
		} else if q, ok := strings.CutPrefix(args[i], "--query="); ok {
			opts.InitialQuery = q
		}
	}
	return opts
}

func parseSharedOptions(args []string) tui.Options {
	opts := tui.Options{
		InitialMode: tui.ModeShared,
	}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--query" && i+1 < len(args):
			i++
			opts.InitialQuery = args[i]
		case strings.HasPrefix(args[i], "--query="):
			opts.InitialQuery = strings.TrimPrefix(args[i], "--query=")
		case args[i] == "--dir" && i+1 < len(args):
			i++
			opts.SharedDir = args[i]
		case strings.HasPrefix(args[i], "--dir="):
			opts.SharedDir = strings.TrimPrefix(args[i], "--dir=")
		}
	}
	return opts
}

func parseTabsOptions(args []string) tui.Options {
	opts := tui.Options{
		InitialMode: tui.ModeTabs,
	}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--new":
			opts.OpenNew = true
		case (args[i] == "--query" || args[i] == "-q") && i+1 < len(args):
			i++
			opts.InitialQuery = args[i]
		case strings.HasPrefix(args[i], "--query="):
			opts.InitialQuery = strings.TrimPrefix(args[i], "--query=")
		case strings.HasPrefix(args[i], "-q="):
			opts.InitialQuery = strings.TrimPrefix(args[i], "-q=")
		case (args[i] == "--session" || args[i] == "-s") && i+1 < len(args):
			i++
			opts.Target = args[i]
		case strings.HasPrefix(args[i], "--session="):
			opts.Target = strings.TrimPrefix(args[i], "--session=")
		case strings.HasPrefix(args[i], "-s="):
			opts.Target = strings.TrimPrefix(args[i], "-s=")
		}
	}
	return opts
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// tmuxCLIAdapter bridges *tmux.Client (context-taking) to cli.TmuxClient.
type tmuxCLIAdapter struct {
	client  *tmux.Client
	backend *tuiBackend
}

func (a *tmuxCLIAdapter) BinaryPath() string {
	if bin := os.Getenv("TMUX_BIN"); bin != "" {
		return bin
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	return ""
}

func (a *tmuxCLIAdapter) ServerRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return a.client.ServerRunning(ctx)
}

func (a *tmuxCLIAdapter) CurrentSession() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	name, err := a.client.CurrentSession(ctx)
	if err != nil || name == "" {
		return "", false
	}
	return name, true
}

func (a *tmuxCLIAdapter) Snapshot() (tmux.ServerSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.Snapshot(ctx)
}

func (a *tmuxCLIAdapter) ListSessions() ([]tmux.Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.ListSessions(ctx)
}

func (a *tmuxCLIAdapter) ListWindows(session string) ([]tmux.Window, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	all, err := a.client.ListWindows(ctx)
	if err != nil || session == "" {
		return all, err
	}
	filtered := make([]tmux.Window, 0, len(all))
	for _, w := range all {
		if w.SessionName == session {
			filtered = append(filtered, w)
		}
	}
	return filtered, nil
}

func (a *tmuxCLIAdapter) ListPanes(session string) ([]tmux.Pane, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	var all []tmux.Pane
	var err error
	if a.backend != nil {
		snap, serr := a.backend.ResolveSnapshot(ctx)
		all, err = snap.Panes, serr
	} else {
		all, err = a.client.ListPanes(ctx)
	}
	if err != nil || session == "" {
		return all, err
	}
	filtered := make([]tmux.Pane, 0, len(all))
	for _, p := range all {
		if p.SessionName == session {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}

func (a *tmuxCLIAdapter) SetPaneOption(pane, option, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.SetPaneOption(ctx, pane, option, value)
}

func (a *tmuxCLIAdapter) UnsetPaneOption(pane, option string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.UnsetPaneOption(ctx, pane, option)
}

func (a *tmuxCLIAdapter) SendKeys(target, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.SendKeys(ctx, target, text, true)
}

func (a *tmuxCLIAdapter) CapturePane(target string, lines int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.CapturePane(ctx, target, lines)
}

func (a *tmuxCLIAdapter) SwitchTo(target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	if session, rest, ok := strings.Cut(target, ":"); ok {
		if win, pane, hasPane := strings.Cut(rest, "."); hasPane {
			return a.client.FocusPane(ctx, session, win, pane)
		}
		return a.client.FocusPane(ctx, session, rest, "")
	}
	return a.client.SwitchToSpace(ctx, target)
}

func (a *tmuxCLIAdapter) NewSession(name, cwd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.NewSession(ctx, name, cwd)
}

func (a *tmuxCLIAdapter) RenameSession(oldName, newName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.RenameSession(ctx, oldName, newName)
}

func (a *tmuxCLIAdapter) KillSession(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.KillSession(ctx, name)
}

func (a *tmuxCLIAdapter) NewWindow(session, cwd, label string, cmd []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxTimeout)
	defer cancel()
	return a.client.NewWindow(ctx, session, cwd, label, cmd...)
}

// tuiBackend implements tui.Backend, tui.ResurrectBackend, and tui.OutcomesBackend
// over *tmux.Client, internal/screen, internal/model, and internal/resurrect.
type tuiBackend struct {
	client     *tmux.Client
	arbitrator *model.Arbitrator
	store      *resurrect.Store
	git        *gitinfo.Collector
	wt         *worktree.Manager
	now        func() time.Time
	dirExists  func(string) bool
	lookPath   func(string) (string, error)
	selfPath   string

	mu                 sync.Mutex
	lastScreenAt       time.Time
	cachedScreens      map[string]model.ScreenReading
	lastTitlesAt       time.Time
	cachedTitles       map[string]string
	cachedTitleQueries map[string]model.TitleQuery
	lastOutcomes       []model.OutcomeEntry
	hasOutcomes        bool
}

func isExistingDir(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func newTUIBackend(client *tmux.Client) *tuiBackend {
	self, _ := os.Executable()
	if self == "" {
		self = "tmux-spaces"
	}
	return &tuiBackend{
		client:             client,
		arbitrator:         model.NewArbitrator(),
		store:              resurrect.DefaultStore(),
		git:                gitinfo.NewCollector(),
		wt:                 worktree.NewManager(),
		now:                time.Now,
		dirExists:          isExistingDir,
		lookPath:           exec.LookPath,
		selfPath:           self,
		cachedScreens:      make(map[string]model.ScreenReading),
		cachedTitles:       make(map[string]string),
		cachedTitleQueries: make(map[string]model.TitleQuery),
	}
}

func (b *tuiBackend) LoadSpaces(ctx context.Context) ([]model.Space, error) {
	_, spaces, err := b.loadResolved(ctx)
	if err == nil && b.git != nil {
		b.git.AttachCached(spaces)
	}
	return spaces, err
}

func (b *tuiBackend) LoadGitStatus(ctx context.Context, cwds []string, force bool) map[string]*model.GitInfo {
	if b.git == nil {
		return nil
	}
	res := b.git.FetchGit(ctx, cwds, force)
	if len(res) > 0 && b.wt != nil && b.wt.Store != nil {
		if recs, err := b.wt.Store.Load(); err == nil && len(recs) > 0 {
			byPath := make(map[string]worktree.Record, len(recs))
			for _, rec := range recs {
				byPath[canonicalPathClean(rec.Path)] = rec
			}
			for _, g := range res {
				if g == nil || g.RepoRoot == "" {
					continue
				}
				if rec, ok := byPath[canonicalPathClean(g.RepoRoot)]; ok {
					g.Managed = rec.Managed
					if g.BaseBranch == "" && rec.BaseBranch != "" {
						g.BaseBranch = rec.BaseBranch
					}
				}
			}
		}
	}
	return res
}

func (b *tuiBackend) LoadPRStatus(ctx context.Context, gitByCWD map[string]*model.GitInfo, force bool) map[string]*model.GitInfo {
	if b.git == nil {
		return nil
	}
	return b.git.FetchPR(ctx, gitByCWD, force)
}

// ResolveSnapshot returns a tmux.ServerSnapshot whose panes have been enriched
// through the full three-layer pipeline (OSC 7501 / @spaces_* options ->
// harness hooks -> screen reader) so CLI commands (`status-bar`, `panes`,
// `space list`, `next`, `wait`) match the TUI's detected harness and status.
func (b *tuiBackend) ResolveSnapshot(ctx context.Context) (tmux.ServerSnapshot, error) {
	snap, spaces, err := b.loadResolved(ctx)
	if err != nil {
		return snap, err
	}
	resolved := make(map[string]model.Pane, len(snap.Panes))
	for _, sp := range spaces {
		for _, p := range sp.Panes {
			resolved[p.ID] = p
		}
	}
	for i := range snap.Panes {
		if rp, ok := resolved[snap.Panes[i].ID]; ok {
			snap.Panes[i].SpacesState = modelStatusString(rp.Status)
			snap.Panes[i].SpacesApp = rp.App
			snap.Panes[i].SpacesMsg = rp.Text
			if rp.ConversationTitle != "" {
				snap.Panes[i].SpacesTitle = rp.ConversationTitle
			}
		}
	}
	return snap, nil
}

func (b *tuiBackend) loadResolved(ctx context.Context) (tmux.ServerSnapshot, []model.Space, error) {
	snap, err := b.client.Snapshot(ctx)
	if err != nil {
		return snap, nil, err
	}
	now := b.now()
	nowMs := now.UnixMilli()
	spaces := model.BuildSpaces(snap, nowMs)

	var hookSnap *model.HookSnapshot
	if hs, err := model.FetchHooks(ctx); err == nil && hs.OK {
		hookSnap = &hs
		b.mu.Lock()
		b.lastOutcomes = append([]model.OutcomeEntry(nil), hs.Outcomes...)
		b.hasOutcomes = true
		b.mu.Unlock()
	}

	// Seed unresolved pane apps from hooks before screen capture so Layer 3
	// can evaluate panes whose process command is generic (e.g. node/2.1.76).
	model.SeedAppsFromHooks(spaces, hookSnap)

	readings := b.screenReadings(ctx, spaces, now)
	b.arbitrator.Apply(spaces, hookSnap, readings, nowMs)
	b.populateSessionStoreTitles(ctx, spaces, now)
	newlyDone := b.arbitrator.TakeNewlyDone()
	b.syncScreenStateToTmux(ctx, spaces, readings, newlyDone, nowMs)
	if len(newlyDone) > 0 {
		b.recordScreenOutcomes(ctx, spaces, newlyDone, nowMs)
	}
	return snap, spaces, nil
}

const screenWorkingHeartbeatMs = int64(30_000)

var returnedShellStaleOptions = []string{
	"@spaces_state",
	"@spaces_app",
	"@spaces_profile",
	"@spaces_kind",
	"@spaces_msg",
	"@spaces_ts",
	"@spaces_source",
	"@spaces_title",
	"@spaces_resume_session",
	"@spaces_resume_agent",
	"@spaces_prior_outcome",
}

// syncScreenStateToTmux persists screen-detected working and working->done
// transitions onto @spaces_state/@spaces_ts/@spaces_source so short-lived
// tmux-spaces invocations (status-bar refreshes and inbox popups) share turn
// state and keep completed turns in DONE for the full 10-minute doneTTLMs
// before decaying to IDLE. It also clears stale @spaces_* options on panes
// that have returned to an interactive shell after an agent exited (e.g. via
// Ctrl+D) so pane borders and resurrect snapshots stay accurate.
func (b *tuiBackend) syncScreenStateToTmux(ctx context.Context, spaces []model.Space, readings map[string]model.ScreenReading, newlyDone []string, nowMs int64) {
	if b.client == nil {
		return
	}
	doneSet := make(map[string]bool, len(newlyDone))
	for _, id := range newlyDone {
		doneSet[id] = true
	}
	tsStr := strconv.FormatInt(nowMs, 10)
	writeScreenState := func(paneID, state string) {
		_ = b.client.SetPaneOption(ctx, paneID, "@spaces_state", state)
		_ = b.client.SetPaneOption(ctx, paneID, "@spaces_ts", tsStr)
		_ = b.client.SetPaneOption(ctx, paneID, "@spaces_source", "screen")
	}
	for _, sp := range spaces {
		for _, p := range sp.Panes {
			if p.IsDead {
				continue
			}
			if p.ReturnedToShell {
				for _, opt := range returnedShellStaleOptions {
					_ = b.client.UnsetPaneOption(ctx, p.ID, opt)
				}
				continue
			}
			if !p.IsAgent() {
				continue
			}
			if doneSet[p.ID] {
				writeScreenState(p.ID, "done")
				continue
			}
			reading, hasReading := readings[p.ID]
			if !hasReading || reading.SkipStateUpdate || reading.OSCAuthority || model.IsAuthorityApp(p.App) {
				continue
			}
			if p.Status == model.StatusWorking && reading.VisibleWorking &&
				(p.BaseStatus != model.StatusWorking || p.BaseAgeMs <= 0 || p.BaseAgeMs >= screenWorkingHeartbeatMs) {
				writeScreenState(p.ID, "working")
			}
		}
	}
}

func (b *tuiBackend) recordScreenOutcomes(ctx context.Context, spaces []model.Space, paneIDs []string, nowMs int64) {
	want := make(map[string]bool, len(paneIDs))
	for _, id := range paneIDs {
		want[id] = true
	}
	for _, sp := range spaces {
		for _, p := range sp.Panes {
			if !want[p.ID] || !p.IsAgent() {
				continue
			}
			sid := strings.TrimSpace(p.ResumeSession)
			if p.Hook != nil && strings.TrimSpace(p.Hook.SessionID) != "" {
				sid = strings.TrimSpace(p.Hook.SessionID)
			} else if resurrect.CanonicalAgent(p.App) == "antigravity" && resurrect.ProcAntigravitySessionFunc != nil && p.PID > 0 {
				if liveSID := strings.TrimSpace(resurrect.ProcAntigravitySessionFunc(p.PID)); liveSID != "" {
					sid = liveSID
				}
			}
			entry := model.OutcomeEntry{
				Session:   sp.Name,
				Pane:      strings.TrimPrefix(p.ID, "%"),
				Agent:     resurrect.CanonicalAgent(p.App),
				App:       p.App,
				Profile:   p.Profile,
				Outcome:   "done",
				Text:      strings.TrimSpace(p.Text),
				Title:     strings.TrimSpace(p.ConversationTitle),
				SessionID: sid,
				CWD:       p.CWD,
				TsMs:      nowMs,
			}
			if entry.Text == "" {
				entry.Text = "turn complete"
			}
			if err := model.RecordOutcome(ctx, entry); err == nil {
				b.mu.Lock()
				b.lastOutcomes = append([]model.OutcomeEntry{entry}, b.lastOutcomes...)
				b.hasOutcomes = true
				b.mu.Unlock()
			}
		}
	}
}

func (b *tuiBackend) populateSessionStoreTitles(ctx context.Context, spaces []model.Space, now time.Time) {
	agyCounts := make(map[string]int)
	for _, sp := range spaces {
		for _, p := range sp.Panes {
			if p.IsDead || !p.IsAgent() {
				continue
			}
			if resurrect.CanonicalAgent(p.App) == "antigravity" {
				agyCounts[p.CWD]++
			}
		}
	}

	var queries []model.TitleQuery
	var skipped []string
	for si := range spaces {
		for pi := range spaces[si].Panes {
			p := &spaces[si].Panes[pi]
			if p.IsDead || !p.IsAgent() {
				continue
			}
			isAgy := resurrect.CanonicalAgent(p.App) == "antigravity"
			sid := strings.TrimSpace(p.ResumeSession)
			if p.Hook != nil && strings.TrimSpace(p.Hook.SessionID) != "" {
				sid = strings.TrimSpace(p.Hook.SessionID)
			} else if isAgy && resurrect.ProcAntigravitySessionFunc != nil && p.PID > 0 {
				// Prefer the live open conversation/presence FD over @spaces_resume_session,
				// which may be empty or hold an older initial launch ID.
				if liveSID := strings.TrimSpace(resurrect.ProcAntigravitySessionFunc(p.PID)); liveSID != "" {
					sid = liveSID
					p.ResumeSession = liveSID
				}
			}
			if strings.TrimSpace(p.ConversationTitle) != "" {
				continue
			}
			// When multiple Antigravity panes share a CWD and one lacks a session ID,
			// do not fall back to the CWD's newest conversation (which would give
			// multiple panes the same title); let it fall back to OSC/screen/window.
			if isAgy && sid == "" && agyCounts[p.CWD] > 1 {
				skipped = append(skipped, p.ID)
				continue
			}
			queries = append(queries, model.TitleQuery{
				Key:       p.ID,
				Agent:     p.App,
				CWD:       p.CWD,
				SessionID: sid,
			})
		}
	}
	if len(queries) == 0 {
		if len(skipped) > 0 {
			b.mu.Lock()
			for _, id := range skipped {
				delete(b.cachedTitles, id)
				delete(b.cachedTitleQueries, id)
			}
			b.mu.Unlock()
		}
		return
	}

	b.mu.Lock()
	for _, id := range skipped {
		delete(b.cachedTitles, id)
		delete(b.cachedTitleQueries, id)
	}
	useCache := !b.lastTitlesAt.IsZero() && now.Sub(b.lastTitlesAt) < screenCaptureInterval
	if useCache {
		for _, q := range queries {
			prevQ, ok := b.cachedTitleQueries[q.Key]
			if !ok || prevQ != q {
				useCache = false
				break
			}
			if _, ok := b.cachedTitles[q.Key]; !ok {
				useCache = false
				break
			}
		}
	}
	var titles map[string]string
	if useCache {
		titles = make(map[string]string, len(b.cachedTitles))
		for k, v := range b.cachedTitles {
			titles[k] = v
		}
		b.mu.Unlock()
	} else {
		b.mu.Unlock()
		fetched, err := model.FetchConversationTitles(ctx, queries)
		b.mu.Lock()
		if err == nil && fetched != nil {
			for _, q := range queries {
				b.cachedTitles[q.Key] = fetched[q.Key]
				b.cachedTitleQueries[q.Key] = q
			}
		}
		b.lastTitlesAt = now
		titles = make(map[string]string, len(b.cachedTitles))
		for k, v := range b.cachedTitles {
			titles[k] = v
		}
		b.mu.Unlock()
	}

	for si := range spaces {
		for pi := range spaces[si].Panes {
			p := &spaces[si].Panes[pi]
			if strings.TrimSpace(p.ConversationTitle) == "" {
				if t := strings.TrimSpace(titles[p.ID]); t != "" {
					p.ConversationTitle = t
				}
			}
		}
	}
}

func modelStatusString(s model.Status) string {
	switch s {
	case model.StatusNeedsYou:
		return "blocked"
	case model.StatusFailed:
		return "failed"
	case model.StatusDone:
		return "done"
	case model.StatusWorking:
		return "working"
	case model.StatusQuiet:
		return "quiet"
	case model.StatusNewOutput:
		return "new_output"
	default:
		return "idle"
	}
}

func (b *tuiBackend) screenReadings(ctx context.Context, spaces []model.Space, now time.Time) map[string]model.ScreenReading {
	b.mu.Lock()
	if !b.lastScreenAt.IsZero() && now.Sub(b.lastScreenAt) < screenCaptureInterval {
		cached := make(map[string]model.ScreenReading, len(b.cachedScreens))
		for k, v := range b.cachedScreens {
			cached[k] = v
		}
		b.mu.Unlock()
		return cached
	}
	b.mu.Unlock()

	readings := make(map[string]model.ScreenReading)
	captured := 0
	for _, sp := range spaces {
		for _, pane := range sp.Panes {
			if captured >= maxCapturePanes {
				break
			}
			if pane.IsDead || !screen.AgentIsKnown(pane.App) {
				continue
			}
			viewport, err := b.client.CapturePane(ctx, pane.ID, defaultCaptureLines)
			if err != nil {
				continue
			}
			captured++
			detected := screen.Detect(pane.App, viewport, pane.Title)
			readings[pane.ID] = model.ScreenReading{
				App:             pane.App,
				State:           detected.State.String(),
				VisibleIdle:     detected.VisibleIdle,
				VisibleBlocker:  detected.VisibleBlocker,
				VisibleWorking:  detected.VisibleWorking,
				SkipStateUpdate: detected.SkipStateUpdate,
				OSCAuthority:    screen.OSCAuthority(pane.App),
				Title:           detected.Title,
			}
		}
	}

	b.mu.Lock()
	b.lastScreenAt = now
	b.cachedScreens = readings
	b.mu.Unlock()
	return readings
}

func (b *tuiBackend) CapturePreviews(ctx context.Context, paneIDs []string, _ int) map[string]string {
	out := make(map[string]string, len(paneIDs))
	for _, id := range paneIDs {
		if id == "" {
			continue
		}
		text, err := b.client.CapturePaneStyled(ctx, id)
		if err != nil {
			continue
		}
		out[id] = text
	}
	return out
}

func (b *tuiBackend) LoadHookDetail(ctx context.Context, session string, paneNum int) (*model.HookDetail, error) {
	snap, err := model.FetchHookDetail(ctx, session, paneNum)
	if err != nil {
		return nil, err
	}
	if !snap.OK || snap.Record == nil {
		return nil, fmt.Errorf("no hook detail for %s pane %d", session, paneNum)
	}
	return snap.Record, nil
}

func (b *tuiBackend) LoadHarnesses(ctx context.Context) ([]model.HarnessEntry, error) {
	file, err := model.FetchHarnesses(ctx)
	if err != nil {
		return nil, err
	}
	return file.Agents, nil
}

func (b *tuiBackend) OpenSpace(ctx context.Context, name string) error {
	return b.client.SwitchToSpace(ctx, name)
}

func (b *tuiBackend) FocusPane(ctx context.Context, session, windowID, paneID string) error {
	return b.client.FocusPane(ctx, session, windowID, paneID)
}

func (b *tuiBackend) CreateSpace(ctx context.Context, name, cwd string) error {
	return b.client.NewSession(ctx, name, cwd)
}

func (b *tuiBackend) RenameSpace(ctx context.Context, oldName, newName string) error {
	return b.client.RenameSession(ctx, oldName, newName)
}

func (b *tuiBackend) RenameWindow(ctx context.Context, windowID, newName string) error {
	return b.client.RenameWindow(ctx, windowID, newName)
}

var (
	_ tui.PreviewBackend      = (*tuiBackend)(nil)
	_ tui.WindowManageBackend = (*tuiBackend)(nil)
)

func (b *tuiBackend) NewWindow(ctx context.Context, session, cwd, name string) error {
	return b.client.NewWindow(ctx, session, cwd, name)
}

func (b *tuiBackend) KillWindow(ctx context.Context, windowID string) error {
	return b.client.KillWindow(ctx, windowID)
}

func (b *tuiBackend) KillSpace(ctx context.Context, name string) error {
	if err := b.client.KillSession(ctx, name); err != nil {
		return err
	}
	// Explicitly closing a space removes its saved snapshot so it does not
	// immediately reappear under Exited (matching zellij-spaces).
	_ = b.store.Delete(name)
	return nil
}

func (b *tuiBackend) LoadExited(ctx context.Context) ([]model.ExitedSpace, error) {
	running := map[string]bool{}
	if snap, err := b.client.Snapshot(ctx); err == nil {
		for _, s := range snap.Sessions {
			running[s.Name] = true
		}
	}
	return b.store.ListExited(running)
}

func (b *tuiBackend) RestoreSpace(ctx context.Context, name string) error {
	snap, err := b.store.Load(name)
	if err != nil {
		return err
	}
	return resurrect.Restore(ctx, b.client, snap, resurrect.RestoreOptions{
		SelfPath: b.selfPath,
		Switch:   true,
	})
}

func (b *tuiBackend) DeleteExited(_ context.Context, name string) error {
	return b.store.Delete(name)
}

func (b *tuiBackend) LaunchAgent(ctx context.Context, session, cwd, agent, profile string) error {
	if session == "" {
		var err error
		session, err = b.client.CurrentSession(ctx)
		if err != nil {
			return fmt.Errorf("could not determine current space: %w", err)
		}
	}
	label := agent
	var cmd []string
	if profile != "" {
		if profile != "default" {
			label = profile
		}
		zjArgv := model.SpacesBinary()
		cmd = append([]string{b.selfPath, "__shim", "--profile", profile, agent}, zjArgv...)
		cmd = append(cmd, "__shim-profile", agent+"/"+profile)
	} else {
		bin := agent
		if bin == "cursor" {
			bin = "cursor-agent"
		}
		cmd = []string{b.selfPath, "__shim", agent, bin}
	}
	return b.client.NewWindow(ctx, session, cwd, label, cmd...)
}

func (b *tuiBackend) LoadOutcomes(ctx context.Context) ([]model.OutcomeEntry, error) {
	b.mu.Lock()
	if b.hasOutcomes {
		out := append([]model.OutcomeEntry(nil), b.lastOutcomes...)
		b.mu.Unlock()
		return out, nil
	}
	b.mu.Unlock()
	hs, err := model.FetchHooks(ctx)
	if err != nil {
		return nil, err
	}
	if !hs.OK {
		return nil, nil
	}
	out := append([]model.OutcomeEntry(nil), hs.Outcomes...)
	b.mu.Lock()
	b.lastOutcomes = append([]model.OutcomeEntry(nil), out...)
	b.hasOutcomes = true
	b.mu.Unlock()
	return out, nil
}

func (b *tuiBackend) OpenOutcome(ctx context.Context, outcome model.OutcomeEntry) error {
	agent := resurrect.CanonicalAgent(outcome.Agent)
	if agent == "" {
		agent = resurrect.CanonicalAgent(outcome.App)
	}
	if agent == "" {
		agent = strings.TrimSpace(outcome.Agent)
	}
	if agent == "" {
		agent = strings.TrimSpace(outcome.App)
	}
	if agent == "" {
		return fmt.Errorf("outcome has no agent to open")
	}

	targetSession := ""
	wantSession := strings.TrimSpace(outcome.Session)
	if sessions, err := b.client.ListSessions(ctx); err == nil {
		for _, s := range sessions {
			if wantSession != "" && s.Name == wantSession {
				targetSession = s.Name
				break
			}
		}
		if targetSession == "" {
			if cur, err := b.client.CurrentSession(ctx); err == nil && cur != "" {
				targetSession = cur
			} else if len(sessions) > 0 {
				targetSession = sessions[0].Name
			}
		}
	} else {
		cur, curErr := b.client.CurrentSession(ctx)
		if curErr != nil {
			return fmt.Errorf("could not determine target space: %w", curErr)
		}
		targetSession = cur
	}
	if targetSession == "" {
		return fmt.Errorf("could not determine target space")
	}

	cwd := strings.TrimSpace(outcome.CWD)
	if cwd != "" && b.dirExists != nil && !b.dirExists(cwd) {
		cwd = ""
	}

	profile := strings.TrimSpace(outcome.Profile)
	sessionID := strings.TrimSpace(outcome.SessionID)
	harnessPath := ""
	if b.lookPath != nil {
		if p, err := b.lookPath("harness"); err == nil && p != "" {
			harnessPath = p
		}
	}
	cmd := buildOutcomeOpenCommand(agent, profile, sessionID, cwd, strings.TrimSpace(outcome.Title), strings.TrimSpace(outcome.Outcome), b.selfPath, harnessPath)
	if len(cmd) == 0 {
		return fmt.Errorf("could not build resume command for %s", agent)
	}

	label := agent
	if profile != "" && profile != "default" {
		label = profile
	}
	if err := b.client.NewWindow(ctx, targetSession, cwd, label, cmd...); err != nil {
		return err
	}
	// Remove the reopened conversation from recent outcomes so it moves cleanly
	// into the live idle/working/done lifecycle without duplicating when closed.
	_ = model.RemoveOutcome(ctx, outcome)
	b.removeCachedOutcome(outcome)
	if cur, err := b.client.CurrentSession(ctx); err != nil || cur != targetSession {
		if err := b.client.SwitchToSpace(ctx, targetSession); err != nil {
			return err
		}
	}
	return nil
}

func (b *tuiBackend) removeCachedOutcome(outcome model.OutcomeEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lastOutcomes) == 0 {
		return
	}
	sid := strings.TrimSpace(outcome.SessionID)
	kept := make([]model.OutcomeEntry, 0, len(b.lastOutcomes))
	for _, o := range b.lastOutcomes {
		if sid != "" && strings.TrimSpace(o.SessionID) == sid {
			continue
		}
		if sid == "" && o.Session == outcome.Session && o.Pane == outcome.Pane && o.TsMs == outcome.TsMs {
			continue
		}
		kept = append(kept, o)
	}
	b.lastOutcomes = kept
}

var _ tui.ConversationsBackend = (*tuiBackend)(nil)

func (b *tuiBackend) LoadConversations(ctx context.Context, refresh, includeArchived bool) (model.ConversationSnapshot, error) {
	return model.FetchConversations(ctx, refresh, includeArchived)
}

func (b *tuiBackend) SearchConversations(ctx context.Context, req model.ConversationSearchRequest) ([]model.ConversationSearchHit, error) {
	resp, err := model.SearchConversations(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Hits, nil
}

func (b *tuiBackend) OpenConversation(ctx context.Context, conv model.ConversationEntry, forceNewTab bool) error {
	_ = forceNewTab
	agent := resurrect.CanonicalAgent(conv.Agent)
	if agent == "" {
		agent = resurrect.CanonicalAgent(conv.App)
	}
	if agent == "" {
		agent = model.CanonicalConversationAgent(conv.Agent)
	}
	if agent == "" {
		agent = strings.TrimSpace(conv.Agent)
	}
	if agent == "" {
		agent = strings.TrimSpace(conv.App)
	}
	if agent == "" {
		return fmt.Errorf("conversation has no agent to open")
	}

	// Open in the current tmux session by default, falling back to conv.Space
	// or the first running session.
	targetSession := ""
	if cur, err := b.client.CurrentSession(ctx); err == nil && strings.TrimSpace(cur) != "" {
		targetSession = strings.TrimSpace(cur)
	}
	if targetSession == "" {
		if sessions, err := b.client.ListSessions(ctx); err == nil {
			wantSpace := strings.TrimSpace(conv.Space)
			for _, s := range sessions {
				if wantSpace != "" && s.Name == wantSpace {
					targetSession = s.Name
					break
				}
			}
			if targetSession == "" && len(sessions) > 0 {
				targetSession = sessions[0].Name
			}
		}
	}
	if targetSession == "" {
		return fmt.Errorf("could not determine target space")
	}

	cwd := strings.TrimSpace(conv.CWD)
	if cwd != "" && b.dirExists != nil && !b.dirExists(cwd) {
		cwd = ""
	}

	profile := strings.TrimSpace(conv.Profile)
	sessionID := conv.ResumeSessionID()
	harnessPath := ""
	if b.lookPath != nil {
		if p, err := b.lookPath("harness"); err == nil && p != "" {
			harnessPath = p
		}
	}

	cmd := buildConversationOpenCommand(agent, profile, sessionID, cwd, strings.TrimSpace(conv.Title), b.selfPath, harnessPath)
	if len(cmd) == 0 {
		return fmt.Errorf("could not build resume command for %s", agent)
	}

	label := agent
	if profile != "" && profile != "default" {
		label = profile
	}
	if err := b.client.NewWindow(ctx, targetSession, cwd, label, cmd...); err != nil {
		return err
	}
	// Best-effort overlay update so space and last_opened_ms stay fresh.
	_ = model.RecordConversationOpen(ctx, conv, targetSession, b.now().UnixMilli())
	if cur, err := b.client.CurrentSession(ctx); err != nil || cur != targetSession {
		if err := b.client.SwitchToSpace(ctx, targetSession); err != nil {
			return err
		}
	}
	return nil
}

func buildConversationOpenCommand(agent, profile, sessionID, cwd, title, selfPath, harnessPath string) []string {
	return buildOutcomeOpenCommand(agent, profile, sessionID, cwd, title, "", selfPath, harnessPath)
}

var _ tui.SharedBackend = (*tuiBackend)(nil)

func (b *tuiBackend) LoadShared(_ context.Context, dir string) (model.SharedSnapshot, error) {
	return model.LoadShared(dir, homeDir())
}

func (b *tuiBackend) OpenSharedFile(ctx context.Context, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("empty file path")
	}
	targetSession := ""
	if cur, err := b.client.CurrentSession(ctx); err == nil && strings.TrimSpace(cur) != "" {
		targetSession = strings.TrimSpace(cur)
	}
	if targetSession == "" {
		if sessions, err := b.client.ListSessions(ctx); err == nil && len(sessions) > 0 {
			targetSession = sessions[0].Name
		}
	}
	if targetSession == "" {
		return fmt.Errorf("could not determine target space")
	}

	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		if b.lookPath != nil {
			for _, cand := range []string{"nvim", "vim", "vi"} {
				if p, err := b.lookPath(cand); err == nil && p != "" {
					editor = cand
					break
				}
			}
		}
		if editor == "" {
			editor = "vi"
		}
	}
	cwd := filepath.Dir(path)
	label := filepath.Base(path)
	if strings.EqualFold(label, "SKILL.md") {
		if dir := filepath.Base(cwd); dir != "" && dir != "." && dir != "/" {
			label = dir
		}
	}
	script := editor + " " + shellQuoteWord(path)
	return b.client.NewWindow(ctx, targetSession, cwd, label, "/bin/sh", "-c", script)
}

// isHarnessManagedAgent reports whether agent is registered in the `harness`
// CLI (so it supports `harness <agent> profile <profile> [resume args...]`).
func isHarnessManagedAgent(agent string) bool {
	switch agent {
	case "antigravity", "claude", "cline", "codex", "copilot", "deepseek",
		"fx", "gemini", "grok", "kilo", "kimi", "kimi-cli", "mcode",
		"muse", "opencode", "pi", "qwen", "vibe":
		return true
	default:
		return false
	}
}

// buildOutcomeOpenCommand builds the tmux window command that reopens a recent
// outcome's conversation in a new tab with the right agent and harness profile,
// then clears pane status and drops into an interactive shell on exit so
// closing the harness does not close the tmux tab.
//
// For harness-managed agents (when `harness` is available), it wraps
// `harness <agent> profile <profile|default>` inside `spaces __resume-wrap` so
// the exact profile binary, shadow home, and resume flags (`--conversation`,
// `--resume`, `resume`, `--session`, etc.) are applied natively without double
// shimming. For non-harness agents, it falls back to `resurrect.BuildPaneCommand`.
func buildOutcomeOpenCommand(agent, profile, sessionID, cwd, title, priorOutcome, selfPath, harnessPath string) []string {
	var inner []string
	if harnessPath != "" && isHarnessManagedAgent(agent) {
		prof := profile
		if prof == "" {
			prof = "default"
		}
		baseCmd := shellQuoteWord(harnessPath) + " " + shellQuoteWord(agent) + " profile " + shellQuoteWord(prof)
		encoded := base64.StdEncoding.EncodeToString([]byte(baseCmd))
		zj := model.SpacesBinary()
		inner = append(append([]string(nil), zj...), "__resume-wrap", encoded)
		if sessionID != "" {
			inner = append(inner, sessionID)
		}
	} else {
		inner = resurrect.BuildPaneCommand(resurrect.PaneSnapshot{
			Agent:     agent,
			SessionID: sessionID,
			Profile:   profile,
			CWD:       cwd,
			Title:     title,
		}, resurrect.RestoreOptions{
			SelfPath: selfPath,
		})
	}
	return wrapOutcomeInShell(inner, priorOutcome, selfPath)
}

func wrapOutcomeInShell(cmd []string, priorOutcome, selfPath string) []string {
	if len(cmd) == 0 {
		return nil
	}
	parts := make([]string, len(cmd))
	for i, a := range cmd {
		parts[i] = shellQuoteWord(a)
	}
	script := strings.Join(parts, " ")
	if strings.EqualFold(strings.TrimSpace(priorOutcome), "failed") {
		script = "SPACES_PRIOR_OUTCOME=failed " + script
	}
	if strings.TrimSpace(selfPath) != "" {
		script += "; " + shellQuoteWord(selfPath) + " status clear >/dev/null 2>&1"
	}
	script += `; exec "${SHELL:-/bin/sh}"`
	return []string{"/bin/sh", "-c", script}
}

func shellQuoteWord(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=' || r == '@') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
