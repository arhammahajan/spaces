package screen

import "testing"

func TestExtractTitleFromViewports(t *testing.T) {
	t.Run("codex status line and prompt fallback", func(t *testing.T) {
		withStatus := "• Done.\n\n" +
			"› Write tests for @filename\n\n" +
			"  gpt-5.4 high · ~/dev/dots · Remove extra T3Code providers\n"
		d := Detect("codex", withStatus, "")
		if d.Title != "Remove extra T3Code providers" {
			t.Errorf("codex status title = %q, want Remove extra T3Code providers", d.Title)
		}

		promptOnly := "› Inspect the tmux-spaces conversation title priority\n\n" +
			"• Working on it...\n" +
			"  gpt-5.4 high · ~/dev/model-router\n"
		d2 := Detect("codex", promptOnly, "")
		if d2.Title != "Inspect the tmux-spaces conversation title priority" {
			t.Errorf("codex prompt fallback = %q", d2.Title)
		}
	})

	t.Run("muse status line", func(t *testing.T) {
		viewport := "  ❯ \n\n" +
			"  muse-spark · high · ~ · Researching Muse Conversation Titles   9.3k/256.0k (4%)\n"
		d := Detect("muse", viewport, "")
		if d.Title != "Researching Muse Conversation Titles" {
			t.Errorf("muse title = %q", d.Title)
		}
	})

	t.Run("opencode top tab bar and prompt box", func(t *testing.T) {
		withTabBar := "  ‹2  OpenCode Zen Default   Orca Tmux-Spaces Redesign   +    ● 2   /status\n" +
			"   ┃\n" +
			"   ┃  update the inbox preview\n" +
			"   ┃\n"
		d := Detect("opencode", withTabBar, "")
		if d.Title != "Orca Tmux-Spaces Redesign" {
			t.Errorf("opencode tab title = %q", d.Title)
		}

		promptBoxOnly := "   ┃\n" +
			"   ┃  Fix the fallback title extraction in opencode\n" +
			"   ┃  Build  gpt-5.4 OpenAI · high\n" +
			"   ╹▀▀▀▀▀▀\n"
		d2 := Detect("opencode", promptBoxOnly, "")
		if d2.Title != "Fix the fallback title extraction in opencode" {
			t.Errorf("opencode prompt box title = %q", d2.Title)
		}
	})

	t.Run("antigravity prompt", func(t *testing.T) {
		viewport := "────────────────────────────────────────────────────────────────────────────────\n" +
			"> currently the tmux plugin i believe is capturing window title of the harness\n" +
			"  to get the working title.\n\n" +
			"● Thinking...\n"
		d := Detect("antigravity", viewport, "")
		if d.Title != "currently the tmux plugin i believe is capturing window title of the harness to get the working title." {
			t.Errorf("antigravity prompt title = %q", d.Title)
		}
	})

	t.Run("claude prompt ignores suggestion placeholder", func(t *testing.T) {
		viewport := "❯ Implement screen reader title fallback\n" +
			"● Running tests\n" +
			"───────────────\n" +
			"❯ Try \"refactor view.go\"\n" +
			"───────────────\n"
		d := Detect("claude-code", viewport, "")
		if d.Title != "Implement screen reader title fallback" {
			t.Errorf("claude prompt title = %q", d.Title)
		}
	})

	t.Run("deepseek prompt fallback", func(t *testing.T) {
		viewport := "❯ Add deepseek screen manifest rules\n" +
			"Done.\n" +
			"───────────────\n" +
			"❯\n" +
			"───────────────\n" +
			"deepseek-reasoner · ~/dev/spaces · /help\n"
		d := Detect("dsh-tui", viewport, "")
		if d.Title != "Add deepseek screen manifest rules" {
			t.Errorf("deepseek prompt title = %q", d.Title)
		}
	})
}
