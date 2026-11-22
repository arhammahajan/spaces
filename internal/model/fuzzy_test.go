package model

import (
	"reflect"
	"testing"
)

func TestFuzzyMatchBasicAndRanking(t *testing.T) {
	// Empty pattern matches everything.
	if s, pos, ok := FuzzyMatch("", "anything"); !ok || s != 0 || len(pos) != 0 {
		t.Fatalf("empty pattern = (%d, %v, %v)", s, pos, ok)
	}
	// Non-match rejected cleanly.
	if _, _, ok := FuzzyMatch("xyz", "abc"); ok {
		t.Fatal("expected non-match")
	}

	// Prefix beats mid-string match.
	sPrefix, posPrefix, ok1 := FuzzyMatch("ref", "Refactor gateway")
	sMid, _, ok2 := FuzzyMatch("ref", "fix the ref later")
	if !ok1 || !ok2 {
		t.Fatalf("expected matches: ok1=%v ok2=%v", ok1, ok2)
	}
	if !reflect.DeepEqual(posPrefix, []int{0, 1, 2}) {
		t.Errorf("posPrefix = %v, want [0 1 2]", posPrefix)
	}
	if sPrefix <= sMid {
		t.Errorf("prefix score (%d) should beat mid-string (%d)", sPrefix, sMid)
	}

	// Word-boundary acronym match beats scattered match.
	sAcronym, posAcronym, okA := FuzzyMatch("rg", "refactor-gateway")
	sScattered, _, okS := FuzzyMatch("rg", "arising")
	if !okA || !okS {
		t.Fatalf("expected acronym (%v) and scattered (%v) matches", okA, okS)
	}
	if !reflect.DeepEqual(posAcronym, []int{0, 9}) {
		t.Errorf("posAcronym = %v, want [0 9]", posAcronym)
	}
	if sAcronym <= sScattered {
		t.Errorf("word-boundary score (%d) should beat scattered (%d)", sAcronym, sScattered)
	}

	// CamelCase boundary bonus.
	sCamel, posCamel, okC := FuzzyMatch("mr", "modelRouter")
	if !okC || !reflect.DeepEqual(posCamel, []int{0, 5}) {
		t.Errorf("camel match = (%d, %v, %v), want [0 5]", sCamel, posCamel, okC)
	}

	// Consecutive beats non-consecutive.
	sConsec, _, _ := FuzzyMatch("gate", "gateway")
	sGapped, _, _ := FuzzyMatch("gate", "g-a-t-e-way")
	if sConsec <= sGapped {
		t.Errorf("consecutive (%d) should beat gapped (%d)", sConsec, sGapped)
	}

	// Unicode-safe rune indices.
	_, posUni, okU := FuzzyMatch("café", "Fix Café Retry")
	if !okU || !reflect.DeepEqual(posUni, []int{4, 5, 6, 7}) {
		t.Errorf("unicode match = (%v, %v), want [4 5 6 7]", posUni, okU)
	}
}

func TestParseConversationQuery(t *testing.T) {
	agent, tokens := ParseConversationQuery("  @agy   rate   limit ")
	if agent != "antigravity" || !reflect.DeepEqual(tokens, []string{"rate", "limit"}) {
		t.Fatalf("got agent=%q tokens=%v", agent, tokens)
	}

	agent, tokens = ParseConversationQuery("@claude-code backoff")
	if agent != "claude" || !reflect.DeepEqual(tokens, []string{"backoff"}) {
		t.Fatalf("got agent=%q tokens=%v", agent, tokens)
	}

	agent, tokens = ParseConversationQuery("@dsh")
	if agent != "deepseek" || len(tokens) != 0 {
		t.Fatalf("got agent=%q tokens=%v", agent, tokens)
	}

	agent, tokens = ParseConversationQuery("just words")
	if agent != "" || !reflect.DeepEqual(tokens, []string{"just", "words"}) {
		t.Fatalf("got agent=%q tokens=%v", agent, tokens)
	}
}

func TestFuzzyScoreConversationWeightsAndMultiTokenAND(t *testing.T) {
	nowMs := int64(1700000100000)

	cTitle := ConversationEntry{
		Agent:       "claude",
		App:         "claude-code",
		Profile:     "work",
		Title:       "Refactor gateway retries",
		CWD:         "/home/u/dev/model-router",
		Branch:      "feat/retry",
		Preview:     "initial prompt",
		UpdatedAtMs: nowMs - 10000,
	}
	cPreviewOnly := ConversationEntry{
		Agent:       "claude",
		App:         "claude-code",
		Title:       "Unrelated chat",
		CWD:         "/home/u/dev/other",
		Preview:     "Refactor gateway retries in proxy",
		UpdatedAtMs: nowMs - 10000,
	}

	sTitle, hlTitle, ok1 := FuzzyScoreConversation(cTitle, []string{"ref", "gate"}, nowMs)
	sPrev, hlPrev, ok2 := FuzzyScoreConversation(cPreviewOnly, []string{"ref", "gate"}, nowMs)
	if !ok1 || !ok2 {
		t.Fatalf("expected both to match: ok1=%v ok2=%v", ok1, ok2)
	}
	if sTitle <= sPrev {
		t.Errorf("title match (%d) should outrank preview-only match (%d)", sTitle, sPrev)
	}
	if len(hlTitle) == 0 {
		t.Errorf("expected title highlight indices, got %v", hlTitle)
	}
	if len(hlPrev) != 0 {
		t.Errorf("preview-only match should not highlight unrelated title, got %v", hlPrev)
	}

	// Multi-token AND across different fields (title + project + branch).
	if _, _, ok := FuzzyScoreConversation(cTitle, []string{"retries", "model-router", "feat/retry"}, nowMs); !ok {
		t.Error("expected multi-field AND match")
	}
	// Missing token rejects entire conversation.
	if _, _, ok := FuzzyScoreConversation(cTitle, []string{"retries", "nonexistent"}, nowMs); ok {
		t.Error("expected missing token to fail AND match")
	}

	// Recency tie-break when metadata matches identically.
	cOlder := cTitle
	cOlder.UpdatedAtMs = nowMs - 40*24*3600*1000
	sNew, _, _ := FuzzyScoreConversation(cTitle, []string{"gateway"}, nowMs)
	sOld, _, _ := FuzzyScoreConversation(cOlder, []string{"gateway"}, nowMs)
	if sNew <= sOld {
		t.Errorf("newer chat (%d) should beat older identical match (%d)", sNew, sOld)
	}
}
