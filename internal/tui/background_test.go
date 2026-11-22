package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

func bgTestSpaces(now time.Time) []model.Space {
	nowMs := now.UnixMilli()
	return []model.Space{
		{
			Name:      "work",
			IsCurrent: true,
			Status:    model.StatusWorking,
			CWD:       "/home/u/work",
			Counts:    model.Counts{Working: 1, Done: 1},
			Panes: []model.Pane{
				{
					ID:                "%1",
					NumericID:         1,
					WindowID:          "@1",
					WindowIndex:       0,
					WindowName:        "claude",
					App:               "claude-code",
					Command:           "claude",
					ConversationTitle: "Implement ATT-5 and ATT-9",
					Status:            model.StatusWorking,
					Text:              "waiting on: go test ./...",
					CWD:               "/home/u/work",
					AgeMs:             15_000,
					Hook: &model.HookSummary{
						Agent:         "claude",
						App:           "claude-code",
						State:         "working",
						Event:         "stop",
						Text:          "waiting on: go test ./...",
						WaitingOn:     "waiting on: go test ./...",
						Model:         "claude-opus-4",
						ChildCount:    1,
						Background:    3,
						FiniteActive:  2,
						ServiceActive: 2,
						HasDetail:     true,
						Children: []model.HookChild{
							{
								ID:       "sub-1",
								Kind:     "Explore",
								Role:     "subagent",
								Category: "finite",
								State:    "working",
								SinceMs:  nowMs - 45_000,
								TsMs:     nowMs - 5_000,
								Text:     "subagent: search auth handlers",
							},
						},
						BackgroundTasks: []model.HookChild{
							{
								ID:       "bg-test",
								Kind:     "local_bash",
								Role:     "command",
								Category: "finite",
								State:    "working",
								Command:  "go test ./...",
								SinceMs:  nowMs - 14_000,
								TsMs:     nowMs - 1_000,
								Text:     "go test ./...",
							},
							{
								ID:       "bg-lint",
								Kind:     "local_bash",
								Role:     "command",
								Category: "finite",
								State:    "failed",
								Outcome:  "exit 1",
								Command:  "golangci-lint run",
								SinceMs:  nowMs - 20_000,
								TsMs:     nowMs - 12_000,
								EndedMs:  nowMs - 12_000,
								Text:     "golangci-lint run",
							},
							{
								ID:       "bg-dev",
								Kind:     "local_bash",
								Role:     "service",
								Category: "service",
								State:    "working",
								Command:  "npm run dev",
								SinceMs:  nowMs - 240_000,
								TsMs:     nowMs - 2_000,
								Text:     "npm run dev",
							},
							{
								ID:       "cron-1",
								Kind:     "cron",
								Role:     "schedule",
								Category: "schedule",
								State:    "working",
								Schedule: "*/5 * * * *",
								SinceMs:  nowMs - 120_000,
								TsMs:     nowMs - 2_000,
								Text:     "Check CI status",
							},
						},
					},
				},
				{
					ID:                "%2",
					NumericID:         2,
					WindowID:          "@2",
					WindowIndex:       1,
					WindowName:        "web",
					App:               "claude-code",
					Command:           "claude",
					ConversationTitle: "Update landing page",
					Status:            model.StatusDone,
					Text:              "Landing page updated",
					CWD:               "/home/u/work",
					AgeMs:             30_000,
					Hook: &model.HookSummary{
						Agent:         "claude",
						App:           "claude-code",
						State:         "done",
						Event:         "stop",
						Text:          "Landing page updated",
						Background:    1,
						FiniteActive:  0,
						ServiceActive: 1,
						HasDetail:     true,
						BackgroundTasks: []model.HookChild{
							{
								ID:       "bg-vite",
								Kind:     "local_bash",
								Role:     "service",
								Category: "service",
								State:    "working",
								Command:  "vite --port 5173",
								SinceMs:  nowMs - 300_000,
								TsMs:     nowMs - 30_000,
								Text:     "vite --port 5173",
							},
						},
					},
				},
			},
		},
	}
}

func TestSpacesInlineBackgroundExpandCollapse(t *testing.T) {
	fixedNow := time.UnixMilli(1700000500000)
	spaces := bgTestSpaces(fixedNow)
	fb := &fakeBackend{spaces: spaces}
	m := New(fb, Options{HomeDir: "/home/u", Now: func() time.Time { return fixedNow }})
	m.spaces = spaces
	m.width, m.height = 130, 30

	// Collapsed by default: primary row shows badge with expand chevron, no inline child rows yet.
	collapsedView := ansi.Strip(m.View())
	if !strings.Contains(collapsedView, "▸ +1 sub · +2 bg · 2 svcs") {
		t.Fatalf("expected collapsed background badge, got:\n%s", collapsedView)
	}
	if !strings.Contains(collapsedView, "▸ 1 svc") {
		t.Fatalf("expected service-only badge on done pane, got:\n%s", collapsedView)
	}
	if strings.Contains(collapsedView, "├─") || strings.Contains(collapsedView, "└─") {
		t.Fatalf("inline background rows should not render while collapsed:\n%s", collapsedView)
	}

	// Select pane %1 (row 1) and press 'e' to expand inline background work.
	m.selected = 1
	m = update(m, keyRunes("e"))
	expandedView := ansi.Strip(m.View())
	for _, want := range []string{
		"▾ +1 sub · +2 bg · 2 svcs",
		"Explore",
		"search auth handlers",
		"running",
		"45s",
		"go test ./...",
		"14s",
		"svc",
		"npm run dev",
		"service",
		"4m",
		"cron */5 * * * *",
		"Check CI status",
		"scheduled",
		"2m",
		"golangci-lint run",
		"failed · exit 1",
		"8s",
		"├─",
		"└─",
	} {
		if !strings.Contains(expandedView, want) {
			t.Errorf("expanded Spaces view missing %q:\n%s", want, expandedView)
		}
	}

	// Pressing Space on pane %1 with background work collapses it (not the whole space).
	m = update(m, keyRunes(" "))
	recollapsed := ansi.Strip(m.View())
	if strings.Contains(recollapsed, "golangci-lint run") {
		t.Errorf("expected Space on pane with background work to collapse inline rows:\n%s", recollapsed)
	}
	if m.collapsed["work"] {
		t.Errorf("Space on pane with background work must not fold the parent space")
	}
}

func TestInboxInlineBackgroundExpandArrowsAndMouseOffset(t *testing.T) {
	fixedNow := time.UnixMilli(1700000500000)
	spaces := bgTestSpaces(fixedNow)
	fb := &fakeBackend{spaces: spaces}
	m := New(fb, Options{InitialMode: ModeInbox, HomeDir: "/home/u", Now: func() time.Time { return fixedNow }})
	m = update(m, tea.WindowSizeMsg{Width: 130, Height: 30})
	m = update(m, spacesMsg{spaces: spaces})

	// Order in Inbox: WORKING (%1, idx 0), DONE (%2, idx 1).
	// Move to %2 (idx 1) and expand with Right arrow -> shows running vite service inline under DONE agent.
	m = update(m, keyType(tea.KeyDown))
	m = update(m, keyType(tea.KeyRight))
	v := ansi.Strip(m.View())
	for _, want := range []string{"▾ 1 svc", "└─", "vite --port 5173", "service", "5m"} {
		if !strings.Contains(v, want) {
			t.Errorf("expanded Inbox view for done agent missing %q:\n%s", want, v)
		}
	}

	// Collapse %2 with Left arrow and verify it hides the inline row.
	m = update(m, keyType(tea.KeyLeft))
	if strings.Contains(ansi.Strip(m.View()), "vite --port 5173") {
		t.Errorf("Left arrow should collapse inline background rows")
	}

	// Expand WORKING (%1, idx 0) with 'l' and click DONE (%2, idx 1) below its expanded inline rows.
	m = update(m, keyType(tea.KeyUp))
	m = update(m, keyRunes("l"))
	layout := m.inboxLayout(130, 30)
	entries := m.buildInboxListEntries(m.inboxItems())
	doneY := -1
	for i, e := range entries {
		if !e.isHeader && !e.isSpacer && e.itemIdx == 1 && e.subLine == 0 {
			doneY = layout.topY + i
			break
		}
	}
	if doneY < 0 {
		t.Fatalf("could not locate done item in expanded inbox list")
	}
	m = update(m, tea.MouseMsg{X: 10, Y: doneY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if !m.Quitting() || len(fb.focused) != 1 || fb.focused[0] != "work/@2/%2" {
		t.Fatalf("click below expanded pane: quitting=%v focused=%v", m.Quitting(), fb.focused)
	}
}

func TestHookDetailOverlayShowsWaitingAndBackgroundCategories(t *testing.T) {
	fixedNow := time.UnixMilli(1700000500000)
	spaces := bgTestSpaces(fixedNow)
	p := spaces[0].Panes[0]
	detail := &model.HookDetail{
		HookSummary:     *p.Hook,
		Prompt:          "Run tests and start dev server",
		Children:        p.Hook.Children,
		BackgroundTasks: p.Hook.BackgroundTasks,
		Background:      p.Hook.Background,
	}
	fb := &fakeBackend{spaces: spaces, hook: detail}
	m := New(fb, Options{HomeDir: "/home/u", Now: func() time.Time { return fixedNow }})
	m.spaces = spaces
	m.width, m.height = 120, 36
	m.selected = 1
	u, _ := m.Update(keyRunes("H"))
	m = u.(Model)
	m.hookDetail = detail
	m.hookLoading = false

	plain := ansi.Strip(m.View())
	for _, want := range []string{
		"waiting on",
		"waiting on: go test ./...",
		"subagents & finite jobs (3)",
		"Explore",
		"go test ./...",
		"golangci-lint run",
		"failed · exit 1",
		"services, watchers & schedules (2)",
		"npm run dev",
		"cron */5 * * * *",
		"Check CI status",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("hook detail overlay missing %q:\n%s", want, plain)
		}
	}
}

func TestSearchMatchesBackgroundCommandsAndSubagents(t *testing.T) {
	fixedNow := time.UnixMilli(1700000500000)
	spaces := bgTestSpaces(fixedNow)
	if _, ok := model.PaneScore(spaces[0].Panes[0], "golangci-lint"); !ok {
		t.Errorf("PaneScore should match background command text")
	}
	if _, ok := model.PaneScore(spaces[0].Panes[1], "vite"); !ok {
		t.Errorf("PaneScore should match running service command")
	}
	if !inboxMatchesQuery(spaces[0], spaces[0].Panes[0], "search auth handlers") {
		t.Errorf("inboxMatchesQuery should match subagent description")
	}
}
