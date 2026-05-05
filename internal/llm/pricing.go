package llm

import "strings"

// Pricing is the per-1M-token rate sheet for one model. Cache rates are
// optional — when zero, the model is assumed to bill cache reads/writes at
// the same rate as fresh input.
type Pricing struct {
	InputPer1M      float64
	OutputPer1M     float64
	CacheReadPer1M  float64 // 0 → falls back to InputPer1M
	CacheWritePer1M float64 // 0 → falls back to InputPer1M
	// Known is set when a model has an explicit pricing entry in our
	// table. Use this to distinguish "$0 because the model is free /
	// local" from "$0 because we don't have pricing data".
	Known bool
}

// Cost converts a Usage record to USD using this pricing.
func (p Pricing) Cost(u Usage) float64 {
	cr := p.CacheReadPer1M
	if cr == 0 {
		cr = p.InputPer1M
	}
	cw := p.CacheWritePer1M
	if cw == 0 {
		cw = p.InputPer1M
	}
	return (float64(u.InputTokens)*p.InputPer1M +
		float64(u.OutputTokens)*p.OutputPer1M +
		float64(u.CacheReadTokens)*cr +
		float64(u.CacheCreationTokens)*cw) / 1_000_000
}

// PricingFor returns the pricing entry for a given model ID. Free model
// detection: any ":free" suffix (OpenRouter convention) and a few local-
// provider sentinels return Known=true with all-zero rates so the caller
// can attribute "$0 because free" correctly.
func PricingFor(model string) Pricing {
	if model == "" {
		return Pricing{}
	}
	low := strings.ToLower(model)
	if strings.HasSuffix(low, ":free") || isLocalSentinel(low) {
		return Pricing{Known: true}
	}
	if p, ok := prices[model]; ok {
		p.Known = true
		return p
	}
	// OpenRouter often serves the same logical model with the provider
	// prefix (e.g. "anthropic/claude-sonnet-4.6"). Strip the prefix and
	// retry.
	if i := strings.Index(model, "/"); i > 0 {
		if p, ok := prices[model[i+1:]]; ok {
			p.Known = true
			return p
		}
	}
	return Pricing{}
}

func isLocalSentinel(s string) bool {
	switch s {
	case "default", // llamacpp / vllm placeholder
		"":
		return true
	}
	// Ollama tags use a single-segment name like "llama3.3" or "qwen2.5-coder:32b"
	// when run locally — anything we don't have a paid price for and that looks
	// like a tag rather than a vendor-prefixed ID is treated as local/free.
	if strings.HasPrefix(s, "llama") || strings.HasPrefix(s, "qwen") ||
		strings.HasPrefix(s, "deepseek-r") || strings.HasPrefix(s, "mistral") ||
		strings.HasPrefix(s, "codellama") || strings.HasPrefix(s, "phi") ||
		strings.HasPrefix(s, "gemma") {
		// Only local-treat these when they look like Ollama tags (no slash).
		// Cloud variants always have provider prefixes.
		if !strings.Contains(s, "/") {
			// But cloud Groq/Mistral entries also use bare names — those are
			// in the explicit price table and matched above. So if we got
			// here it's not in the table → treat as local/free.
			return true
		}
	}
	return false
}

// prices is the per-provider rate sheet (USD per 1M tokens). Verified
// against vendor pricing pages in early 2026; will need refreshing as
// vendors retune. Unknown-but-paid models contribute $0 to the displayed
// total — the status bar shows a "?" marker so the user knows the figure
// is a floor rather than the actual.
var prices = map[string]Pricing{
	// Anthropic (cache_read = 0.1×input, cache_write = 1.25×input).
	"claude-opus-4-7": {
		InputPer1M: 5, OutputPer1M: 25,
		CacheReadPer1M: 0.50, CacheWritePer1M: 6.25,
	},
	"claude-sonnet-4-6": {
		InputPer1M: 3, OutputPer1M: 15,
		CacheReadPer1M: 0.30, CacheWritePer1M: 3.75,
	},
	"claude-haiku-4-5-20251001": {
		InputPer1M: 1, OutputPer1M: 5,
		CacheReadPer1M: 0.10, CacheWritePer1M: 1.25,
	},
	"claude-haiku-4-5": {
		InputPer1M: 1, OutputPer1M: 5,
		CacheReadPer1M: 0.10, CacheWritePer1M: 1.25,
	},

	// OpenAI (cache_read = 0.5×input).
	"gpt-4o":      {InputPer1M: 2.50, OutputPer1M: 10.00, CacheReadPer1M: 1.25},
	"gpt-4o-mini": {InputPer1M: 0.15, OutputPer1M: 0.60, CacheReadPer1M: 0.075},
	"o3-mini":     {InputPer1M: 1.10, OutputPer1M: 4.40, CacheReadPer1M: 0.55},

	// Groq (no documented cache; rates are paid-tier; free tier returns
	// these tokens at $0 anyway since billing doesn't kick in).
	"llama-3.3-70b-versatile":      {InputPer1M: 0.59, OutputPer1M: 0.79},
	"llama-3.1-8b-instant":         {InputPer1M: 0.05, OutputPer1M: 0.08},
	"openai/gpt-oss-120b":          {InputPer1M: 0.15, OutputPer1M: 0.75},
	"openai/gpt-oss-20b":           {InputPer1M: 0.10, OutputPer1M: 0.50},

	// DeepSeek.
	"deepseek-chat":     {InputPer1M: 0.27, OutputPer1M: 1.10},
	"deepseek-reasoner": {InputPer1M: 0.55, OutputPer1M: 2.19},

	// Mistral.
	"mistral-large-latest": {InputPer1M: 2.00, OutputPer1M: 6.00},
	"codestral-latest":     {InputPer1M: 0.20, OutputPer1M: 0.60},
	"open-mistral-nemo":    {InputPer1M: 0.15, OutputPer1M: 0.15},

	// Cerebras (paid tier; free tier returns $0 since these tokens aren't
	// billed under the free quota — overestimate for paid users only).
	"llama-3.3-70b": {InputPer1M: 0.85, OutputPer1M: 1.20},
	"llama3.1-8b":   {InputPer1M: 0.10, OutputPer1M: 0.10},
	"gpt-oss-120b":  {InputPer1M: 0.30, OutputPer1M: 1.20},

	// Gemini.
	"gemini-2.5-pro":   {InputPer1M: 1.25, OutputPer1M: 10.00},
	"gemini-2.5-flash": {InputPer1M: 0.30, OutputPer1M: 2.50},
	"gemini-2.0-flash": {InputPer1M: 0.10, OutputPer1M: 0.40},
}
