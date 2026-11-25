package agent

import "strings"

// ModelRates defines USD pricing per 1,000,000 tokens for a model family.
type ModelRates struct {
	InputPerM      float64
	OutputPerM     float64
	CacheReadPerM  float64
	CacheWritePerM float64
}

type modelRateRule struct {
	match string
	rates ModelRates
}

// canonicalModelRates maps normalized model prefixes/substrings to USD per 1M tokens.
// Ordered from most specific to least specific.
var canonicalModelRates = []modelRateRule{
	// Anthropic Claude 4.x
	{match: "claude-opus-4-6", rates: ModelRates{InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50, CacheWritePerM: 18.75}},
	{match: "claude-opus-4-5", rates: ModelRates{InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50, CacheWritePerM: 18.75}},
	{match: "claude-opus-4-1", rates: ModelRates{InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50, CacheWritePerM: 18.75}},
	{match: "claude-opus-4", rates: ModelRates{InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50, CacheWritePerM: 18.75}},
	{match: "claude-sonnet-4-6", rates: ModelRates{InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30, CacheWritePerM: 3.75}},
	{match: "claude-sonnet-4-5", rates: ModelRates{InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30, CacheWritePerM: 3.75}},
	{match: "claude-sonnet-4", rates: ModelRates{InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30, CacheWritePerM: 3.75}},
	{match: "claude-haiku-4-5", rates: ModelRates{InputPerM: 1.0, OutputPerM: 5.0, CacheReadPerM: 0.10, CacheWritePerM: 1.25}},
	{match: "claude-haiku-4", rates: ModelRates{InputPerM: 1.0, OutputPerM: 5.0, CacheReadPerM: 0.10, CacheWritePerM: 1.25}},
	// Anthropic Claude 3.x
	{match: "claude-3-7-sonnet", rates: ModelRates{InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30, CacheWritePerM: 3.75}},
	{match: "claude-3-5-sonnet", rates: ModelRates{InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30, CacheWritePerM: 3.75}},
	{match: "claude-3-5-haiku", rates: ModelRates{InputPerM: 0.80, OutputPerM: 4.0, CacheReadPerM: 0.08, CacheWritePerM: 1.0}},
	{match: "claude-3-opus", rates: ModelRates{InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50, CacheWritePerM: 18.75}},
	// OpenAI / Codex
	{match: "gpt-5.4-mini", rates: ModelRates{InputPerM: 0.75, OutputPerM: 4.50, CacheReadPerM: 0.075, CacheWritePerM: 0.75}},
	{match: "gpt-5.4", rates: ModelRates{InputPerM: 2.50, OutputPerM: 15.0, CacheReadPerM: 0.25, CacheWritePerM: 2.50}},
	{match: "gpt-5-codex", rates: ModelRates{InputPerM: 1.25, OutputPerM: 10.0, CacheReadPerM: 0.125, CacheWritePerM: 1.25}},
	{match: "gpt-5-mini", rates: ModelRates{InputPerM: 0.25, OutputPerM: 2.0, CacheReadPerM: 0.025, CacheWritePerM: 0.25}},
	{match: "gpt-5-nano", rates: ModelRates{InputPerM: 0.05, OutputPerM: 0.40, CacheReadPerM: 0.005, CacheWritePerM: 0.05}},
	{match: "gpt-5", rates: ModelRates{InputPerM: 1.25, OutputPerM: 10.0, CacheReadPerM: 0.125, CacheWritePerM: 1.25}},
	{match: "codex-mini", rates: ModelRates{InputPerM: 1.50, OutputPerM: 6.0, CacheReadPerM: 0.375, CacheWritePerM: 1.50}},
	{match: "o4-mini", rates: ModelRates{InputPerM: 1.10, OutputPerM: 4.40, CacheReadPerM: 0.275, CacheWritePerM: 1.10}},
	{match: "o3-mini", rates: ModelRates{InputPerM: 1.10, OutputPerM: 4.40, CacheReadPerM: 0.55, CacheWritePerM: 1.10}},
	{match: "o3", rates: ModelRates{InputPerM: 2.0, OutputPerM: 8.0, CacheReadPerM: 0.50, CacheWritePerM: 2.0}},
	{match: "gpt-4.1-mini", rates: ModelRates{InputPerM: 0.40, OutputPerM: 1.60, CacheReadPerM: 0.10, CacheWritePerM: 0.40}},
	{match: "gpt-4.1-nano", rates: ModelRates{InputPerM: 0.10, OutputPerM: 0.40, CacheReadPerM: 0.025, CacheWritePerM: 0.10}},
	{match: "gpt-4.1", rates: ModelRates{InputPerM: 2.0, OutputPerM: 8.0, CacheReadPerM: 0.50, CacheWritePerM: 2.0}},
	{match: "gpt-4o-mini", rates: ModelRates{InputPerM: 0.15, OutputPerM: 0.60, CacheReadPerM: 0.075, CacheWritePerM: 0.15}},
	{match: "gpt-4o", rates: ModelRates{InputPerM: 2.50, OutputPerM: 10.0, CacheReadPerM: 1.25, CacheWritePerM: 2.50}},
	// Google Gemini
	{match: "gemini-2.5-pro", rates: ModelRates{InputPerM: 1.25, OutputPerM: 10.0, CacheReadPerM: 0.31, CacheWritePerM: 1.25}},
	{match: "gemini-2.5-flash", rates: ModelRates{InputPerM: 0.30, OutputPerM: 2.50, CacheReadPerM: 0.075, CacheWritePerM: 0.30}},
	{match: "gemini-2.0-flash", rates: ModelRates{InputPerM: 0.10, OutputPerM: 0.40, CacheReadPerM: 0.025, CacheWritePerM: 0.10}},
	// DeepSeek
	{match: "deepseek-reasoner", rates: ModelRates{InputPerM: 0.55, OutputPerM: 2.19, CacheReadPerM: 0.14, CacheWritePerM: 0.55}},
	{match: "deepseek-chat", rates: ModelRates{InputPerM: 0.27, OutputPerM: 1.10, CacheReadPerM: 0.07, CacheWritePerM: 0.27}},
}

// normalizeModelForPricing strips provider prefixes, "[1m]" tags, dots-to-dashes
// for Claude version strings, and trailing YYYYMMDD date suffixes.
func normalizeModelForPricing(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return ""
	}
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.ReplaceAll(s, "[1m]", "")
	s = strings.TrimSpace(s)
	// Strip trailing -YYYYMMDD date suffix if present.
	if len(s) > 9 && s[len(s)-9] == '-' {
		allDigits := true
		for _, c := range s[len(s)-8:] {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			s = s[:len(s)-9]
		}
	}
	// Normalize claude-sonnet-4.6 -> claude-sonnet-4-6.
	if strings.HasPrefix(s, "claude-") {
		s = strings.ReplaceAll(s, ".", "-")
	}
	return s
}

// LookupModelRates returns the USD-per-1M-token rates for a model name.
func LookupModelRates(model string) (ModelRates, bool) {
	norm := normalizeModelForPricing(model)
	if norm == "" {
		return ModelRates{}, false
	}
	for _, r := range canonicalModelRates {
		if norm == r.match || strings.HasPrefix(norm, r.match+"-") || strings.HasPrefix(norm, r.match+":") || strings.Contains(norm, r.match) {
			return r.rates, true
		}
	}
	return ModelRates{}, false
}

// EstimateCostUSD estimates the USD cost for a TokenUsage given a model name.
// Returns (cost, true) when the model is in the pricing table (even if cost is 0.0),
// and (0, false) when the model is unpriced so callers can distinguish unpriced vs $0.
func EstimateCostUSD(model string, u TokenUsage) (float64, bool) {
	rates, ok := LookupModelRates(model)
	if !ok {
		return 0, false
	}
	outTokens := u.OutputTokens + u.ReasoningTokens
	cost := (float64(u.InputTokens)*rates.InputPerM +
		float64(outTokens)*rates.OutputPerM +
		float64(u.CacheReadTokens)*rates.CacheReadPerM +
		float64(u.CacheWriteTokens)*rates.CacheWritePerM) / 1_000_000.0
	return cost, true
}
