package modelpick

import (
	"reflect"
	"strings"
	"testing"
)

// injectPickerSeams replaces the package seams for one test and restores
// them afterwards. It returns the input reader (so tests can assert the
// non-TTY path consumes nothing) and the captured stderr.
func injectPickerSeams(t *testing.T, tty bool, input string) (*strings.Reader, *strings.Builder) {
	t.Helper()
	oldIn, oldOut, oldTTY := Stdin, Stderr, StdinIsTerminal
	reader := strings.NewReader(input)
	out := &strings.Builder{}
	Stdin = reader
	Stderr = out
	StdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() {
		Stdin, Stderr, StdinIsTerminal = oldIn, oldOut, oldTTY
	})
	return reader, out
}

// pickModels is deliberately unsorted to prove the picker numbers the
// ID-sorted listing.
func pickModels() []Model {
	return []Model{
		{ID: "zai/glm-4.6", ContextWindow: 1000000, Reasoning: "low,high,max", Modalities: []string{"text", "image", "pdf"}},
		{ID: "anthropic/claude-sonnet-4-5", ContextWindow: 200000, MaxOutput: 64000},
		{ID: "openai/gpt-5", ContextWindow: 1200000},
	}
}

func TestPickNumber(t *testing.T) {
	_, out := injectPickerSeams(t, true, "2\n")
	m, ok := Pick(pickModels())
	if !ok || m.ID != "openai/gpt-5" {
		t.Fatalf("pick 2 = %q, %v", m.ID, ok)
	}
	if !strings.Contains(out.String(), "1) anthropic/claude-sonnet-4-5") {
		t.Fatalf("numbered table missing from:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Choice [1-3, filter, Enter=first, q=quit]: ") {
		t.Fatalf("prompt missing from:\n%s", out.String())
	}
}

func TestPickNumberWithoutNewlineAtEOF(t *testing.T) {
	_, _ = injectPickerSeams(t, true, "3")
	m, ok := Pick(pickModels())
	if !ok || m.ID != "zai/glm-4.6" {
		t.Fatalf("pick 3 at EOF = %q, %v", m.ID, ok)
	}
}

func TestPickFilterThenNumber(t *testing.T) {
	_, out := injectPickerSeams(t, true, "GLM\n1\n")
	m, ok := Pick(pickModels())
	if !ok || m.ID != "zai/glm-4.6" {
		t.Fatalf("filter then 1 = %q, %v", m.ID, ok)
	}
	got := out.String()
	if !strings.Contains(got, "Choice [1-3") || !strings.Contains(got, "Choice [1-1") {
		t.Fatalf("both prompts must appear:\n%s", got)
	}
	if strings.Count(got, "zai/glm-4.6") != 2 {
		t.Fatalf("filtered row must re-render once:\n%s", got)
	}
	if strings.Count(got, "anthropic/claude-sonnet-4-5") != 1 {
		t.Fatalf("non-matching row must appear only in the initial table:\n%s", got)
	}
}

func TestPickEmptyPicksFirst(t *testing.T) {
	_, _ = injectPickerSeams(t, true, "\n")
	m, ok := Pick(pickModels())
	if !ok || m.ID != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("enter must pick the first sorted model, got %q, %v", m.ID, ok)
	}
}

func TestPickEmptyAfterFilterPicksFirstMatch(t *testing.T) {
	_, out := injectPickerSeams(t, true, "sonnet\n\n")
	m, ok := Pick(pickModels())
	if !ok || m.ID != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("enter after filter must pick the first match, got %q, %v", m.ID, ok)
	}
	if !strings.Contains(out.String(), "Choice [1-1") {
		t.Fatalf("filtered prompt missing:\n%s", out.String())
	}
}

func TestPickQCancels(t *testing.T) {
	for _, input := range []string{"q\n", "Q\n"} {
		_, out := injectPickerSeams(t, true, input)
		if m, ok := Pick(pickModels()); ok {
			t.Fatalf("%q must cancel, got %q", input, m.ID)
		} else if !strings.Contains(out.String(), "Choice [1-3") {
			t.Fatalf("%q must still show the table and prompt:\n%s", input, out.String())
		}
	}
}

func TestPickEOFWithNothingTypedCancels(t *testing.T) {
	_, out := injectPickerSeams(t, true, "")
	if _, ok := Pick(pickModels()); ok {
		t.Fatal("closed input must cancel")
	}
	if !strings.Contains(out.String(), "Choice [1-3") {
		t.Fatalf("closed input must still show the table and prompt:\n%s", out.String())
	}
}

func TestPickFilterWithoutMatchesCancelsOnEnter(t *testing.T) {
	_, out := injectPickerSeams(t, true, "zzz\n\n")
	if _, ok := Pick(pickModels()); ok {
		t.Fatal("empty filter result plus enter must cancel")
	}
	if strings.Count(out.String(), "MODALITIES") != 2 {
		t.Fatalf("the dead-end filter must re-render the (header-only) table:\n%s", out.String())
	}
}

func TestPickOutOfRangeNumberFallsThroughToFilter(t *testing.T) {
	models := append(pickModels(), Model{ID: "qwen/qwen3-9b", ContextWindow: 100000})
	_, out := injectPickerSeams(t, true, "9\n\n")
	m, ok := Pick(models)
	if !ok || m.ID != "qwen/qwen3-9b" {
		t.Fatalf("out-of-range number must filter by substring, got %q, %v", m.ID, ok)
	}
	if !strings.Contains(out.String(), "1) qwen/qwen3-9b") {
		t.Fatalf("filtered re-render missing:\n%s", out.String())
	}
}

func TestPickNonTerminalIsANoOp(t *testing.T) {
	reader, out := injectPickerSeams(t, false, "1\n")
	m, ok := Pick(pickModels())
	if ok || !reflect.DeepEqual(m, Model{}) {
		t.Fatalf("headless pick must be a no-op, got %+v, %v", m, ok)
	}
	if out.Len() != 0 {
		t.Fatalf("headless pick must print nothing, got:\n%s", out.String())
	}
	if reader.Len() != len("1\n") {
		t.Fatalf("headless pick must not consume input, %d bytes left", reader.Len())
	}
}

func TestPickEmptyListingIsANoOp(t *testing.T) {
	_, out := injectPickerSeams(t, true, "1\n")
	if m, ok := Pick(nil); ok || !reflect.DeepEqual(m, Model{}) {
		t.Fatalf("empty listing must be a no-op, got %+v, %v", m, ok)
	}
	if out.Len() != 0 {
		t.Fatalf("empty listing must print nothing, got:\n%s", out.String())
	}
}
