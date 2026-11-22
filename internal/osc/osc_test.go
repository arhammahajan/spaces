package osc

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestParsePayload(t *testing.T) {
	msg := base64.StdEncoding.EncodeToString([]byte("Approve bash: go test"))
	rec, ok := ParsePayload("state=blocked:app=claude-code:kind=permission:msg=" + msg)
	if !ok {
		t.Fatal("expected valid record")
	}
	if rec.State != "blocked" || rec.App != "claude-code" || rec.Kind != "permission" || rec.Msg != "Approve bash: go test" {
		t.Fatalf("unexpected record: %+v", rec)
	}

	rec2, ok := ParsePayload("state=error:app=codex:profile=work")
	if !ok || rec2.State != "failed" || rec2.App != "codex" || rec2.Profile != "work" {
		t.Fatalf("error should normalize to failed with profile, got %+v (ok=%v)", rec2, ok)
	}

	rec3, ok := ParsePayload("state=clear")
	if !ok || rec3.State != "clear" {
		t.Fatalf("clear should parse, got %+v (ok=%v)", rec3, ok)
	}

	if _, ok := ParsePayload("app=claude"); ok {
		t.Fatal("missing state should be rejected")
	}
	if _, ok := ParsePayload("state=bogus:app=claude"); ok {
		t.Fatal("unknown state should be rejected")
	}
}

func TestScannerSTAndBELAndChunkSplits(t *testing.T) {
	msg := base64.StdEncoding.EncodeToString([]byte("editing main.go"))
	stream := "normal output\x1b]2;Window Title\x07more text" +
		"\x1b]7501;state=working:app=claude:msg=" + msg + "\x1b\\" +
		"in between\x1b]7501;state=done:app=claude\x07tail"

	// Feed 1 byte at a time to exercise every state transition across chunk splits.
	var sc Scanner
	var got []Record
	for i := 0; i < len(stream); i++ {
		sc.Feed([]byte{stream[i]}, func(r Record) {
			got = append(got, r)
		})
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(got), got)
	}
	if got[0].State != "working" || got[0].App != "claude" || got[0].Msg != "editing main.go" {
		t.Fatalf("first record = %+v", got[0])
	}
	if got[1].State != "done" || got[1].App != "claude" || got[1].Msg != "" {
		t.Fatalf("second record = %+v", got[1])
	}
}

func TestScannerIgnoresOversizedOSC(t *testing.T) {
	huge := "\x1b]7501;state=working:app=" + strings.Repeat("a", maxOSCBytes+100) + "\x1b\\"
	valid := "\x1b]7501;state=idle:app=pi\x07"
	var got []Record
	if err := Watch(bytes.NewBufferString(huge+valid), func(r Record) {
		got = append(got, r)
	}); err != nil {
		t.Fatalf("Watch error: %v", err)
	}
	if len(got) != 1 || got[0].State != "idle" || got[0].App != "pi" {
		t.Fatalf("expected only the valid post-overflow record, got %+v", got)
	}
}

func TestSanitizeControlCharsInMsg(t *testing.T) {
	msg := base64.StdEncoding.EncodeToString([]byte("hello\x1b[31m\nworld"))
	rec, ok := ParsePayload("state=working:app=opencode:msg=" + msg)
	if !ok {
		t.Fatal("expected ok")
	}
	if strings.ContainsRune(rec.Msg, '\x1b') || strings.ContainsRune(rec.Msg, '\n') {
		t.Fatalf("control chars leaked into msg: %q", rec.Msg)
	}
}

func TestScannerTmuxPassthroughOSC(t *testing.T) {
	// Tmux DCS passthrough doubles ESC bytes: \x1bPtmux;\x1b\x1b]7501;...\x1b\x1b\\\x1b\\
	stream := "\x1bPtmux;\x1b\x1b]7501;state=working:app=claude\x1b\x1b\\\x1b\\" +
		"\x1bPtmux;\x1b\x1b]7501;state=done:app=claude\x1b\x1b\\\x1b\\"
	var got []Record
	if err := Watch(bytes.NewBufferString(stream), func(r Record) {
		got = append(got, r)
	}); err != nil {
		t.Fatalf("Watch error: %v", err)
	}
	if len(got) != 2 || got[0].State != "working" || got[1].State != "done" {
		t.Fatalf("got %+v, want [working done]", got)
	}
}
