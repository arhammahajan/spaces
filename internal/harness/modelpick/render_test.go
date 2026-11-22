package modelpick

import (
	"fmt"
	"strings"
	"testing"
)

// goldenModels is deliberately unsorted to prove Render sorts by ID
// and never mutates the caller's slice.
func goldenModels() []Model {
	return []Model{
		{ID: "openai/gpt-5", OwnedBy: "model-router", ContextWindow: 1200000, MaxOutput: 4096},
		{ID: "anthropic/claude-sonnet-4-5", ContextWindow: 200000, MaxOutput: 64000},
		{ID: "zai/glm-4.6", ContextWindow: 1000000, MaxOutput: 131072, Reasoning: "low,high,max", Modalities: []string{"text", "image", "pdf"}},
		{ID: "mistral/mystery", OwnedBy: "unknown-owner"},
	}
}

func TestRenderGolden(t *testing.T) {
	// Column widths: ID 27, CTX 4, OUT 4, REASONING 12, MODALITIES
	// unpadded; two-space gutters; "?" for every unknown fact.
	row := func(cells ...string) string {
		return fmt.Sprintf("%-27s  %-4s  %-4s  %-12s  %s", cells[0], cells[1], cells[2], cells[3], cells[4])
	}
	want := strings.Join([]string{
		row("ID", "CTX", "OUT", "REASONING", "MODALITIES"),
		row("anthropic/claude-sonnet-4-5", "200K", "64K", "?", "?"),
		row("mistral/mystery", "?", "?", "?", "?"),
		row("openai/gpt-5", "1.2M", "4K", "?", "?"),
		row("zai/glm-4.6", "1M", "131K", "low,high,max", "text,image,pdf"),
	}, "\n") + "\n"

	if got := Render(goldenModels()); got != want {
		t.Fatalf("render mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderDoesNotMutateInput(t *testing.T) {
	models := goldenModels()
	Render(models)
	if models[0].ID != "openai/gpt-5" {
		t.Fatalf("render must not sort the caller's slice, got %q", models[0].ID)
	}
}

func TestRenderEmpty(t *testing.T) {
	if got, want := Render(nil), "ID  CTX  OUT  REASONING  MODALITIES\n"; got != want {
		t.Fatalf("empty listing must render the bare header, got %q", got)
	}
}

func TestHumanTokens(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "?"},
		{1, "1"},
		{999, "999"},
		{1000, "1K"},
		{4096, "4K"},
		{64000, "64K"},
		{131072, "131K"},
		{200000, "200K"},
		{1000000, "1M"},
		{1200000, "1.2M"},
		{2000000, "2M"},
	}
	for _, c := range cases {
		if got := humanTokens(c.in); got != c.want {
			t.Fatalf("humanTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
