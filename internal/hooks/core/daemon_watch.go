package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DaemonPaneTarget identifies a live tmux pane that may have an attachable
// agent-native daemon or SSE event stream.
type DaemonPaneTarget struct {
	Session       string
	PaneID        string
	PaneNumericID string
	PanePID       int
	Agent         string
	CWD           string
	Profile       string
	Endpoint      string
}

// DaemonChildEvent represents a subagent lifecycle update emitted by a
// daemon watcher.
type DaemonChildEvent struct {
	ID      string
	Type    string
	State   string
	Outcome string
	Text    string
	SinceMs int64
	TsMs    int64
	EndedMs int64
}

// DaemonEvent is the provider-agnostic event emitted by a DaemonWatchFn.
type DaemonEvent struct {
	Agent      string
	App        string
	Source     string
	Event      string
	RawEvent   string
	State      string
	Kind       string
	Text       string
	Title      string
	Prompt     string
	Tool       string
	ToolInput  string
	ToolOutput string
	Reply      string
	Model      string
	SessionID  string
	CWD        string
	Profile    string
	TsMs       int64
	Usage      *Usage
	Child      *DaemonChildEvent
}

// DaemonWatchFn watches one live pane's agent-native daemon stream until ctx
// is cancelled or the stream exits, emitting provider-agnostic DaemonEvents.
type DaemonWatchFn func(ctx context.Context, target DaemonPaneTarget, emit func(DaemonEvent)) error

// DaemonWatchPaneLister lists live panes eligible for daemon watching.
// Wired by cmd/tmux-spaces so internal/hooks/core remains provider-agnostic.
var DaemonWatchPaneLister func(ctx context.Context) ([]DaemonPaneTarget, error)

// DaemonWatchResolver returns the DaemonWatchFn for a canonical agent name,
// or nil when the agent has no daemon watcher. Wired by cmd/tmux-spaces.
var DaemonWatchResolver func(agent string) DaemonWatchFn

// DaemonEventToNormalized converts a provider-agnostic DaemonEvent directly
// into a spacecore normalized event map.
func DaemonEventToNormalized(ev DaemonEvent) map[string]any {
	agent := strings.ToLower(strings.TrimSpace(ev.Agent))
	if alias, ok := Aliases[agent]; ok {
		agent = alias
	}
	if agent == "" {
		agent = "unknown"
	}
	app := strings.TrimSpace(ev.App)
	if app == "" {
		if mapped, ok := AppNames[agent]; ok {
			app = mapped
		} else {
			app = agent
		}
	}
	source := strings.TrimSpace(ev.Source)
	if source == "" {
		source = "daemon_sse"
	}
	event := strings.TrimSpace(ev.Event)
	rawEvent := strings.TrimSpace(ev.RawEvent)
	if event == "" && rawEvent != "" {
		event = CanonicalEvent(rawEvent)
	}
	if rawEvent == "" {
		rawEvent = event
	}

	state := strings.TrimSpace(ev.State)
	kind := strings.TrimSpace(ev.Kind)
	if state == "" && event != "" {
		if derivedState, derivedKind, ok := EventState(event, map[string]any{}); ok {
			state = derivedState
			if kind == "" {
				kind = derivedKind
			}
		}
	}

	ts := ev.TsMs
	if ts <= 0 {
		ts = nowMs()
	}

	tool := truncateRunes(strings.TrimSpace(ev.Tool), MaxModel)
	text := oneLine(ev.Text, MaxText, false)
	if text == "" && tool != "" {
		text = BuildPreview(tool, ev.ToolInput)
	}
	if text == "" && event != "" {
		text = strings.ReplaceAll(event, "_", " ")
	}

	var kindVal any
	if kind != "" {
		kindVal = kind
	}

	var sessionIDVal any
	if sid := strings.TrimSpace(ev.SessionID); sid != "" {
		sessionIDVal = truncateRunes(sid, MaxSession)
	}

	var childVal any
	if ev.Child != nil {
		childTs := ev.Child.TsMs
		if childTs <= 0 {
			childTs = ts
		}
		childSince := ev.Child.SinceMs
		if childSince <= 0 {
			childSince = childTs
		}
		childState := strings.TrimSpace(ev.Child.State)
		if childState == "" {
			childState = "working"
		}
		childID := strings.TrimSpace(ev.Child.ID)
		if childID == "" {
			childID = "child"
		}
		childVal = map[string]any{
			"id":       truncateRunes(childID, MaxSession),
			"type":     truncateRunes(strings.TrimSpace(ev.Child.Type), MaxModel),
			"kind":     BgKindSubagent,
			"category": BgCategoryFinite,
			"state":    childState,
			"outcome":  oneLine(ev.Child.Outcome, 80, false),
			"text":     oneLine(ev.Child.Text, MaxText, false),
			"since_ms": childSince,
			"ts_ms":    childTs,
			"ended_ms": ev.Child.EndedMs,
		}
	}

	out := map[string]any{
		"v":           Version,
		"agent":       agent,
		"app":         app,
		"source":      truncateRunes(source, MaxModel),
		"event":       event,
		"raw_event":   oneLine(rawEvent, MaxModel, false),
		"state":       state,
		"kind":        kindVal,
		"text":        text,
		"title":       fieldStr(ev.Title, MaxText, false),
		"prompt":      fieldStr(ev.Prompt, MaxField, false),
		"tool":        tool,
		"tool_input":  fieldStr(ev.ToolInput, MaxField, false),
		"tool_output": fieldStr(ev.ToolOutput, MaxOutput, true),
		"reply":       fieldStr(ev.Reply, MaxField, true),
		"model":       fieldStr(ev.Model, MaxModel, false),
		"session_id":  sessionIDVal,
		"cwd":         fieldStr(ev.CWD, MaxCWD, false),
		"background":  0,
		"child":       childVal,
		"ts_ms":       ts,
		"seq":         nowNanos(),
	}
	if profile := strings.TrimSpace(ev.Profile); profile != "" {
		out["profile"] = truncateRunes(profile, MaxModel)
	}
	if usage := normalizeUsage(ev.Usage); usage != nil {
		out["usage"] = usage
	}
	return out
}

type activeDaemonWatcher struct {
	cancel context.CancelFunc
}

// DaemonWatchManager reconciles live tmux panes against registered agent
// daemon watchers, starting one goroutine per matching pane identity and
// cancelling watchers cleanly when panes exit or change PID/agent.
type DaemonWatchManager struct {
	lister   func(context.Context) ([]DaemonPaneTarget, error)
	resolver func(string) DaemonWatchFn
	mergeFn  func(session string, pane any, event map[string]any) error

	mu       sync.Mutex
	watchers map[string]*activeDaemonWatcher
	wg       sync.WaitGroup
}

// NewDaemonWatchManager creates a DaemonWatchManager. When mergeFn is nil,
// events are merged via MergeIntoStore.
func NewDaemonWatchManager(
	lister func(context.Context) ([]DaemonPaneTarget, error),
	resolver func(string) DaemonWatchFn,
	mergeFn func(session string, pane any, event map[string]any) error,
) *DaemonWatchManager {
	if mergeFn == nil {
		mergeFn = MergeIntoStore
	}
	return &DaemonWatchManager{
		lister:   lister,
		resolver: resolver,
		mergeFn:  mergeFn,
		watchers: map[string]*activeDaemonWatcher{},
	}
}

func daemonWatchKey(t DaemonPaneTarget) string {
	return t.Session + "|" + t.PaneID + "|" + strconv.Itoa(t.PanePID) + "|" + t.Agent
}

func daemonPaneKey(t DaemonPaneTarget) string {
	if k := strings.TrimSpace(t.PaneNumericID); k != "" {
		return k
	}
	return strings.TrimPrefix(strings.TrimSpace(t.PaneID), "%")
}

// Reconcile lists live panes, starts watchers for newly discovered matching
// panes, and cancels watchers whose pane disappeared or changed PID/agent.
func (m *DaemonWatchManager) Reconcile(ctx context.Context) {
	if m == nil || m.lister == nil || m.resolver == nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	targets, err := m.lister(ctx)
	if err != nil {
		return
	}

	type desiredWatcher struct {
		target  DaemonPaneTarget
		watchFn DaemonWatchFn
	}
	desired := make(map[string]desiredWatcher, len(targets))
	for _, t := range targets {
		t.Session = strings.TrimSpace(t.Session)
		t.PaneID = strings.TrimSpace(t.PaneID)
		t.Agent = strings.ToLower(strings.TrimSpace(t.Agent))
		if alias, ok := Aliases[t.Agent]; ok {
			t.Agent = alias
		}
		if t.Session == "" || t.Agent == "" || (t.PaneID == "" && strings.TrimSpace(t.PaneNumericID) == "") {
			continue
		}
		watchFn := m.resolver(t.Agent)
		if watchFn == nil {
			continue
		}
		key := daemonWatchKey(t)
		desired[key] = desiredWatcher{target: t, watchFn: watchFn}
	}

	m.mu.Lock()
	for key, entry := range m.watchers {
		if _, ok := desired[key]; !ok {
			entry.cancel()
			delete(m.watchers, key)
		}
	}
	for key, want := range desired {
		if _, exists := m.watchers[key]; exists {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		entry := &activeDaemonWatcher{cancel: cancel}
		m.watchers[key] = entry
		m.wg.Add(1)
		go func(key string, entry *activeDaemonWatcher, target DaemonPaneTarget, watchFn DaemonWatchFn, wctx context.Context) {
			defer func() {
				m.mu.Lock()
				if m.watchers[key] == entry {
					delete(m.watchers, key)
				}
				m.mu.Unlock()
				m.wg.Done()
			}()
			paneKey := daemonPaneKey(target)
			emit := func(ev DaemonEvent) {
				if ev.Agent == "" {
					ev.Agent = target.Agent
				}
				if ev.CWD == "" {
					ev.CWD = target.CWD
				}
				if ev.Profile == "" {
					ev.Profile = target.Profile
				}
				norm := DaemonEventToNormalized(ev)
				if norm == nil {
					return
				}
				if err := m.mergeFn(target.Session, paneKey, norm); err != nil {
					logMsg(fmt.Sprintf("daemon watch merge error (%s/%s): %v", target.Session, paneKey, err))
				}
			}
			if err := watchFn(wctx, target, emit); err != nil && wctx.Err() == nil {
				logMsg(fmt.Sprintf("daemon watcher exited (%s): %v", key, err))
			}
		}(key, entry, want.target, want.watchFn, wctx)
	}
	m.mu.Unlock()
}

// Run reconciles immediately and on every interval tick until ctx is done,
// then cancels and waits for all active watchers to exit cleanly.
func (m *DaemonWatchManager) Run(ctx context.Context, interval time.Duration) {
	if m == nil {
		return
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	m.Reconcile(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			m.Reconcile(ctx)
		}
	}
}

func (m *DaemonWatchManager) stopAll() {
	m.mu.Lock()
	for key, entry := range m.watchers {
		entry.cancel()
		delete(m.watchers, key)
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// ActiveCount returns the number of currently active watcher goroutines.
func (m *DaemonWatchManager) ActiveCount() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.watchers)
}
