package agent

import (
	"math"
	"testing"
)

func TestEstimateCostUSD(t *testing.T) {
	u := TokenUsage{
		InputTokens:      100_000,
		OutputTokens:     10_000,
		CacheReadTokens:  500_000,
		CacheWriteTokens: 50_000,
	}

	cost, ok := EstimateCostUSD("claude-sonnet-4-6[1m]", u)
	if !ok {
		t.Fatalf("expected claude-sonnet-4-6[1m] to be priced")
	}
	// 0.1*3 + 0.01*15 + 0.5*0.30 + 0.05*3.75 = 0.3 + 0.15 + 0.15 + 0.1875 = 0.7875
	if math.Abs(cost-0.7875) > 1e-6 {
		t.Fatalf("got cost %.6f, want 0.7875", cost)
	}

	// Date-suffixed + provider-prefixed model
	if _, ok := EstimateCostUSD("anthropic/claude-3-5-sonnet-20241022", u); !ok {
		t.Fatalf("expected date-suffixed claude-3-5-sonnet to be priced")
	}

	// Codex / OpenAI model
	if _, ok := EstimateCostUSD("gpt-5-codex", u); !ok {
		t.Fatalf("expected gpt-5-codex to be priced")
	}

	// Unpriced model returns ok=false
	if c, ok := EstimateCostUSD("unknown-local-model", u); ok || c != 0 {
		t.Fatalf("expected unknown model to be unpriced, got (%.4f, %v)", c, ok)
	}
}
