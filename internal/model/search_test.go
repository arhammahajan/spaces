package model

import (
	"testing"
)

func scorePane(app, window, title, text string, hook *HookSummary) Pane {
	return Pane{App: app, WindowName: window, Title: title, Text: text, Hook: hook, Status: StatusWorking}
}

func TestSpaceScoreRanking(t *testing.T) {
	space := Space{
		Name: "dots", CWD: "/home/me/dev",
		Panes: []Pane{scorePane("opencode", "editor", "t", "editing auth", nil)},
	}
	cases := []struct {
		query string
		want  int
		ok    bool
	}{
		{"dots", 0, true},
		{"dot", 5, true},
		{"ots", 10, true},
		{"dev", 20, true},
		{"auth", 55, true}, // via pane text
		{"missing", 0, false},
		{"", 0, true},
		{"  DOTS  ", 0, true},
	}
	for _, c := range cases {
		got, ok := SpaceScore(space, c.query)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("query %q: got (%d,%v) want (%d,%v)", c.query, got, ok, c.want, c.ok)
		}
	}
	// A pane-only match returns the best pane score.
	if got, ok := SpaceScore(space, "opencode"); !ok || got != 20 {
		t.Errorf("agent query: got (%d,%v)", got, ok)
	}
	// Space without panes and non-matching query.
	if _, ok := SpaceScore(Space{Name: "x"}, "zzz"); ok {
		t.Error("expected no match")
	}
}

func TestPaneScoreRanking(t *testing.T) {
	hook := &HookSummary{Model: "specialmodel", SessionID: "session-1234", Event: "pre_tool", Kind: "permission"}
	p := Pane{
		App: "claude", Profile: "pratham", WindowName: "tab-2", Title: "approve the migration",
		Text: "some output", CWD: "/home/me/dev/dots",
		Hook:   hook,
		Status: StatusNeedsYou,
	}
	cases := []struct {
		query string
		want  int
	}{
		{"claude", 20},
		{"clau", 25},
		{"tab-2", 30},
		{"tab-", 35},
		{"migration", 40},
		{"laud", 45}, // app substring (not covered by earlier rules)
		{"pratham", 46},
		{"b-2", 50}, // window substring
		{"output", 55},
		{"specialmodel", 58},
		{"session-1234", 58},
		{"pre_tool", 58},
		{"permission", 58},
		{"dev", 60},
		{"needs you", 65},
		{"", 0},
	}
	for _, c := range cases {
		got, ok := PaneScore(p, c.query)
		if !ok || got != c.want {
			t.Errorf("query %q: got (%d,%v) want (%d,true)", c.query, got, ok, c.want)
		}
	}
	if _, ok := PaneScore(p, "nope"); ok {
		t.Error("expected no match for 'nope'")
	}
	// Empty pane matches empty query only.
	if _, ok := PaneScore(Pane{}, "x"); ok {
		t.Error("empty pane should not match")
	}
	if got, ok := PaneScore(Pane{}, ""); !ok || got != 0 {
		t.Error("empty query should match")
	}
}

func TestSortSpaces(t *testing.T) {
	mk := func(name string, current bool, status Status) Space {
		var c Counts
		c.Add(status)
		return Space{Name: name, IsCurrent: current, Status: status, Counts: c}
	}
	spaces := []Space{
		mk("zeta", false, StatusIdle),
		mk("alpha", false, StatusNeedsYou),
		mk("beta", false, StatusWorking),
		mk("current", true, StatusIdle),
		mk("Alpha2", false, StatusNeedsYou),
	}
	SortSpaces(spaces)
	want := []string{"current", "alpha", "Alpha2", "beta", "zeta"}
	for i, w := range want {
		if spaces[i].Name != w {
			t.Errorf("position %d: got %s want %s", i, spaces[i].Name, w)
		}
	}
}
