package llm

import (
	"strings"
	"sync"
)

// dynamicPrices holds pricing learned at runtime — typically from a
// provider's /v1/models endpoint. Used for OpenRouter (100+ models we
// can't realistically pre-table) and any other provider that publishes
// rates in its catalogue. Lookup order in PricingFor is: dynamic
// (freshest) → hardcoded → free-suffix detection → unknown.
var dynamicPrices sync.Map // map[string]Pricing

// RegisterDynamicPricing records a model's rate sheet at runtime. Called
// by FetchModels after parsing each provider's /v1/models response. Safe
// for concurrent use.
func RegisterDynamicPricing(model string, p Pricing) {
	if model == "" {
		return
	}
	p.Known = true
	dynamicPrices.Store(model, p)
}

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

// CostWithoutCache returns what u would cost if every cache read and
// cache creation token were billed at the full input rate (i.e. no prompt
// caching). Used to compute caching savings.
func (p Pricing) CostWithoutCache(u Usage) float64 {
	return (float64(u.InputTokens+u.CacheReadTokens+u.CacheCreationTokens)*p.InputPer1M +
		float64(u.OutputTokens)*p.OutputPer1M) / 1_000_000
}

// PricingFor returns the pricing entry for a given model ID.
//
// Lookup order:
//  1. dynamic prices registered at runtime (e.g. from OpenRouter's
//     /v1/models — covers 100+ models without us shipping a table)
//  2. hardcoded prices for major direct-provider models
//  3. free-marker detection: ":free" suffix (OpenRouter convention) and
//     local-provider sentinels (Ollama tags, "default") return zero
//     rates with Known=true so callers attribute "$0 because free"
//  4. otherwise zero with Known=false (the status bar prefixes the
//     total with "≥" so the user knows it's a floor)
func PricingFor(model string) Pricing {
	if model == "" {
		return Pricing{}
	}
	if v, ok := dynamicPrices.Load(model); ok {
		p := v.(Pricing)
		p.Known = true
		return p
	}
	low := strings.ToLower(model)
	if strings.HasSuffix(low, ":free") {
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
	// isLocalSentinel is checked last: cloud providers like Groq and Mistral
	// use bare model names (no "/" prefix) that overlap with Ollama tags.
	// Checking the hardcoded table first ensures we return the real pricing for
	// known-paid models. Any bare name that isn't in the table is legitimately
	// local/free.
	if isLocalSentinel(low) {
		return Pricing{Known: true}
	}
	return Pricing{}
}

// IndicativePricingFor returns "what this model would cost if you were
// paying for it" — useful for showing free-tier users the value they're
// getting. For paid models, returns the same as PricingFor. For :free
// suffix models, strips the suffix and looks up the paid variant. For
// local Ollama tags, maps to the closest cloud equivalent.
func IndicativePricingFor(model string) Pricing {
	actual := PricingFor(model)
	if actual.Known && (actual.InputPer1M > 0 || actual.OutputPer1M > 0) {
		return actual
	}

	// OpenRouter ":free" suffix → look up the paid variant.
	if strings.HasSuffix(model, ":free") {
		base := strings.TrimSuffix(model, ":free")
		if p := PricingFor(base); p.Known {
			return p
		}
	}

	// Local Ollama tag → guess a cloud equivalent.
	if equiv := localToCloud(model); equiv != "" {
		if p := PricingFor(equiv); p.Known {
			return p
		}
	}

	return actual
}

// localToCloud maps Ollama-style local tags to the model ID of a hosted
// equivalent we have pricing for. Returns "" when no good guess exists.
func localToCloud(model string) string {
	low := strings.ToLower(model)
	switch {
	case strings.HasPrefix(low, "llama3.3"), strings.HasPrefix(low, "llama-3.3-70b"):
		return "llama-3.3-70b-versatile" // Groq's price
	case strings.HasPrefix(low, "llama3.1:8b"), strings.HasPrefix(low, "llama-3.1-8b"):
		return "llama-3.1-8b-instant"
	case strings.HasPrefix(low, "deepseek-r"):
		return "deepseek-reasoner"
	case low == "deepseek-v4-flash", strings.HasPrefix(low, "deepseek-chat"):
		return "deepseek-v4-flash"
	case low == "deepseek-v4-pro":
		return "deepseek-v4-pro"
	case strings.HasPrefix(low, "deepseek-v"):
		return "deepseek-v4-flash"
	case strings.HasPrefix(low, "mistral-large"):
		return "mistral-large-latest"
	case strings.HasPrefix(low, "codestral"):
		return "codestral-latest"
	}
	return ""
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

	// DeepSeek. deepseek-chat/deepseek-reasoner are legacy aliases for deepseek-v4-flash.
	"deepseek-v4-flash":  {InputPer1M: 0.14, OutputPer1M: 0.28, Known: true},
	"deepseek-v4-pro":    {InputPer1M: 0.435, OutputPer1M: 0.87, Known: true}, // 75% off until 2026-05-31
	"deepseek-chat":      {InputPer1M: 0.14, OutputPer1M: 0.28, Known: true},  // alias for v4-flash
	"deepseek-reasoner":  {InputPer1M: 0.14, OutputPer1M: 0.28, Known: true},  // alias for v4-flash thinking

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
