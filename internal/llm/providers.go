package llm

// AdapterKind names the wire protocol used to talk to a provider.
type AdapterKind string

const (
	KindAnthropic    AdapterKind = "anthropic"
	KindOpenAICompat AdapterKind = "openai-compat"
)

// ProviderSpec describes a built-in provider preset.
type ProviderSpec struct {
	Name        string      // canonical identifier (lower-case, used in config)
	Label       string      // human-readable name for the wizard
	Description string      // one-line summary
	Kind        AdapterKind // which adapter to use
	BaseURL     string      // empty = SDK default; required for openai-compat unless overridden
	APIKeyEnv   string      // env var with the API key; empty = no auth needed
	NeedsKey    bool        // if true, wizard prompts for a key
	Free        bool        // has a usable free tier
	Local       bool        // talks to localhost (probed by wizard)

	// DefaultModel is what the wizard pre-selects.
	DefaultModel string

	// RecommendedModels is a curated list shown in the wizard. Each entry
	// is [name, label, free?]. Free models bubble to the top.
	RecommendedModels []ModelSuggestion

	// SuggestedMaxSubagents is what the wizard proposes when this provider
	// is chosen for sub-agents (reflects RPM limits on free tiers).
	SuggestedMaxSubagents int

	// SupportsCaching tells the user prompt caching applies (only Anthropic
	// + OpenAI proper today).
	SupportsCaching bool

	// KnownLimits is human-readable text shown in the wizard.
	KnownLimits string
}

type ModelSuggestion struct {
	ID    string
	Label string
	Free  bool
}

// providers is the canonical catalog. Order is wizard-display order: paid
// flagship providers first, then strong free-tier hosted, then local.
var providers = []ProviderSpec{
	{
		Name:         "anthropic",
		Label:        "Anthropic",
		Description:  "Claude Opus / Sonnet / Haiku. Best tool-use compliance and prompt caching.",
		Kind:         KindAnthropic,
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		NeedsKey:     true,
		Free:         false,
		DefaultModel: "claude-sonnet-4-6",
		RecommendedModels: []ModelSuggestion{
			{ID: "claude-opus-4-7", Label: "Opus 4.7 — flagship, planner-grade"},
			{ID: "claude-sonnet-4-6", Label: "Sonnet 4.6 — strong default for sub-agents"},
			{ID: "claude-haiku-4-5-20251001", Label: "Haiku 4.5 — cheap and fast"},
		},
		SuggestedMaxSubagents: 8,
		SupportsCaching:       true,
		KnownLimits:           "Pay-as-you-go. Trial credits on signup. Generous tier-1 RPM.",
	},
	{
		Name:         "openai",
		Label:        "OpenAI",
		Description:  "GPT family with automatic prompt caching.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://api.openai.com/v1",
		APIKeyEnv:    "OPENAI_API_KEY",
		NeedsKey:     true,
		Free:         false,
		DefaultModel: "gpt-5.4",
		RecommendedModels: []ModelSuggestion{
			{ID: "gpt-5.4", Label: "GPT-5.4 — current flagship"},
			{ID: "gpt-5.4-mini", Label: "GPT-5.4 Mini — efficient sub-agent"},
			{ID: "gpt-5.4-nano", Label: "GPT-5.4 Nano — fastest/cheapest"},
			{ID: "gpt-5.2", Label: "GPT-5.2 — proven strong generation"},
			{ID: "gpt-4.1", Label: "GPT-4.1 — stable April 2025"},
			{ID: "gpt-4.1-mini", Label: "GPT-4.1 Mini"},
			{ID: "gpt-4.1-nano", Label: "GPT-4.1 Nano — cheapest 4.1"},
			{ID: "o3", Label: "o3 — flagship reasoning"},
			{ID: "o4-mini", Label: "o4-mini — reasoning at sub-agent budget"},
			{ID: "o3-mini", Label: "o3-mini — compact reasoning"},
			{ID: "gpt-4o", Label: "GPT-4o — legacy stable"},
			{ID: "gpt-4o-mini", Label: "GPT-4o mini — legacy cheap"},
		},
		SuggestedMaxSubagents: 8,
		SupportsCaching:       true,
		KnownLimits:           "Pay-as-you-go. Prompt caching on compatible models saves ~75% on repeated context. Tier-based RPM.",
	},
	{
		Name:         "openrouter",
		Label:        "OpenRouter",
		Description:  "Aggregator: 100+ models from many vendors via one API. Many ':free' models.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://openrouter.ai/api/v1",
		APIKeyEnv:    "OPENROUTER_API_KEY",
		NeedsKey:     true,
		Free:         true,
		DefaultModel: "meta-llama/llama-3.3-70b-instruct:free",
		RecommendedModels: []ModelSuggestion{
			{ID: "meta-llama/llama-3.3-70b-instruct:free", Label: "Llama 3.3 70B (free)", Free: true},
			{ID: "qwen/qwen3-coder:free", Label: "Qwen3 Coder (free, code-specialized)", Free: true},
			{ID: "z-ai/glm-4.5-air:free", Label: "GLM 4.5 Air (free)", Free: true},
			{ID: "openai/gpt-oss-120b:free", Label: "GPT-OSS 120B (free)", Free: true},
			{ID: "anthropic/claude-sonnet-4.6", Label: "Claude Sonnet 4.6 (paid)"},
			{ID: "anthropic/claude-opus-4.7", Label: "Claude Opus 4.7 (paid)"},
			{ID: "openai/gpt-5.4", Label: "GPT-5.4 (paid)"},
			{ID: "x-ai/grok-4", Label: "Grok 4 (paid)"},
			{ID: "google/gemini-2.5-pro-preview", Label: "Gemini 2.5 Pro (paid)"},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Free models: low daily quota, varies by upstream. Set max-subagents conservatively.",
	},
	{
		Name:         "groq",
		Label:        "Groq",
		Description:  "Very fast Llama / GPT-OSS inference on custom hardware. Free tier with RPM limits.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://api.groq.com/openai/v1",
		APIKeyEnv:    "GROQ_API_KEY",
		NeedsKey:     true,
		Free:         true,
		DefaultModel: "llama-3.3-70b-versatile",
		RecommendedModels: []ModelSuggestion{
			{ID: "llama-3.3-70b-versatile", Label: "Llama 3.3 70B (free)", Free: true},
			{ID: "openai/gpt-oss-120b", Label: "GPT-OSS 120B — fast, strong coding (free)", Free: true},
			{ID: "openai/gpt-oss-20b", Label: "GPT-OSS 20B — fastest at ~1000 t/s (free)", Free: true},
			{ID: "llama-3.1-8b-instant", Label: "Llama 3.1 8B Instant (free, ~560 t/s)", Free: true},
			{ID: "meta-llama/llama-4-scout-17b-16e-instruct", Label: "Llama 4 Scout 17B (preview, free)", Free: true},
			{ID: "qwen/qwen3-32b", Label: "Qwen3 32B (preview, free)", Free: true},
			{ID: "groq/compound", Label: "Groq Compound — agentic system (preview, free)", Free: true},
		},
		SuggestedMaxSubagents: 2,
		KnownLimits:           "Free tier: ~30 RPM (production), preview models may have lower limits. DeepSeek and old Qwen models are deprecated — use GPT-OSS or Llama instead.",
	},
	{
		Name:         "huggingface",
		Label:        "HuggingFace",
		Description:  "Routes to Together / Fireworks / Replicate via HF. Small monthly free credit.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://router.huggingface.co/v1",
		APIKeyEnv:    "HF_TOKEN",
		NeedsKey:     true,
		Free:         true,
		DefaultModel: "meta-llama/Llama-3.3-70B-Instruct",
		RecommendedModels: []ModelSuggestion{
			{ID: "meta-llama/Llama-3.3-70B-Instruct", Label: "Llama 3.3 70B", Free: true},
			{ID: "Qwen/Qwen2.5-Coder-32B-Instruct", Label: "Qwen 2.5 Coder 32B", Free: true},
			{ID: "deepseek-ai/DeepSeek-V3", Label: "DeepSeek V3"},
		},
		SuggestedMaxSubagents: 2,
		KnownLimits:           "Free credit: small monthly $. Tool-use compliance varies by upstream model.",
	},
	{
		Name:         "cerebras",
		Label:        "Cerebras",
		Description:  "World's fastest Llama inference. Generous free tier.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://api.cerebras.ai/v1",
		APIKeyEnv:    "CEREBRAS_API_KEY",
		NeedsKey:     true,
		Free:         true,
		DefaultModel: "gpt-oss-120b",
		RecommendedModels: []ModelSuggestion{
			{ID: "gpt-oss-120b", Label: "GPT-OSS 120B — main production model (~3000 t/s)", Free: true},
			{ID: "llama3.1-8b", Label: "Llama 3.1 8B (~2200 t/s, deprecating May 2026)", Free: true},
			{ID: "qwen-3-235b-a22b-instruct-2507", Label: "Qwen3 235B (preview, free)", Free: true},
			{ID: "zai-glm-4.7", Label: "Z.ai GLM 4.7 355B (preview, free)", Free: true},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Free tier: ~30 RPM, 1M tokens/day. World's fastest inference. Note: llama-3.3-70b is no longer available — use gpt-oss-120b instead.",
	},
	{
		Name:         "mistral",
		Label:        "Mistral",
		Description:  "Mistral La Plateforme: Codestral, Mistral Large, etc. Free tier with rate limits.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://api.mistral.ai/v1",
		APIKeyEnv:    "MISTRAL_API_KEY",
		NeedsKey:     true,
		Free:         true,
		DefaultModel: "devstral-small-2507",
		RecommendedModels: []ModelSuggestion{
			{ID: "devstral-small-2507", Label: "Devstral Small 1.1 — coding agent, free", Free: true},
			{ID: "magistral-medium-latest", Label: "Magistral Medium — reasoning flagship, free", Free: true},
			{ID: "mistral-small-latest", Label: "Mistral Small 3.2 — general purpose, free", Free: true},
			{ID: "mistral-large-latest", Label: "Mistral Large (may be deprecated — check docs)"},
			{ID: "codestral-latest", Label: "Codestral — code-specialized (check availability)"},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Free tier: 1 RPS, 500k tokens/min. Note: open-mistral-nemo shutdown June 2025, mistral-large-2411 possibly shutdown May 2026. Use Magistral or Devstral for current best performance.",
	},
	{
		Name:         "deepseek",
		Label:        "DeepSeek",
		Description:  "DeepSeek V4 Flash / V4 Pro from the source. Trial credits + cheap pay-as-you-go.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://api.deepseek.com/v1",
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		NeedsKey:     true,
		Free:         false,
		DefaultModel: "deepseek-v4-flash",
		RecommendedModels: []ModelSuggestion{
			{ID: "deepseek-v4-flash", Label: "DeepSeek V4 Flash — fast, supports thinking mode"},
			{ID: "deepseek-v4-pro", Label: "DeepSeek V4 Pro — premium reasoning, 75% off until May 2026"},
			{ID: "deepseek-chat", Label: "DeepSeek V4 Flash (legacy alias — deepseek-chat)"},
			{ID: "deepseek-reasoner", Label: "DeepSeek R1 (legacy alias — deepseek-reasoner)"},
		},
		SuggestedMaxSubagents: 8,
		KnownLimits:           "Trial credits on signup; pay-as-you-go after. Very cheap. deepseek-chat/deepseek-reasoner are deprecated aliases for deepseek-v4-flash.",
	},
	{
		Name:         "gemini",
		Label:        "Google Gemini",
		Description:  "Gemini 2.5 Pro / Flash via OpenAI-compatible endpoint. Generous free tier.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://generativelanguage.googleapis.com/v1beta/openai",
		APIKeyEnv:    "GEMINI_API_KEY",
		NeedsKey:     true,
		Free:         true,
		DefaultModel: "gemini-2.5-flash",
		RecommendedModels: []ModelSuggestion{
			{ID: "gemini-3.1-pro-preview", Label: "Gemini 3.1 Pro Preview — latest flagship", Free: true},
			{ID: "gemini-3-flash-preview", Label: "Gemini 3 Flash Preview — latest mid", Free: true},
			{ID: "gemini-3.1-flash-lite", Label: "Gemini 3.1 Flash-Lite — stable, lowest latency", Free: true},
			{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro — stable until Oct 2026", Free: true},
			{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash — stable, best price/perf", Free: true},
			{ID: "gemini-2.5-flash-lite", Label: "Gemini 2.5 Flash-Lite — most cost-efficient", Free: true},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Free tier varies by model. Note: gemini-2.0-flash and gemini-2.0-flash-lite were shut down June 1 2026; gemini-1.5-pro/flash fully removed. Use 2.5 or 3.x series.",
	},
	{
		Name:         "ollama",
		Label:        "Ollama (local)",
		Description:  "Local models via `ollama serve` on :11434. No key needed.",
		Kind:         KindOpenAICompat,
		BaseURL:      "http://localhost:11434/v1",
		NeedsKey:     false,
		Free:         true,
		Local:        true,
		DefaultModel: "llama3.3",
		RecommendedModels: []ModelSuggestion{
			{ID: "llama3.3", Label: "Llama 3.3 (run: ollama pull llama3.3)", Free: true},
			{ID: "qwen2.5-coder:32b", Label: "Qwen 2.5 Coder 32B", Free: true},
			{ID: "deepseek-r1:14b", Label: "DeepSeek R1 14B", Free: true},
		},
		SuggestedMaxSubagents: 2,
		KnownLimits:           "Limited by local hardware. Sub-agent parallelism above 2 will queue.",
	},
	{
		Name:         "llamacpp",
		Label:        "llama.cpp (local)",
		Description:  "llama-server on :8080. No key. Single-model server.",
		Kind:         KindOpenAICompat,
		BaseURL:      "http://localhost:8080/v1",
		NeedsKey:     false,
		Free:         true,
		Local:        true,
		DefaultModel: "default",
		RecommendedModels: []ModelSuggestion{
			{ID: "default", Label: "(whatever llama-server has loaded)", Free: true},
		},
		SuggestedMaxSubagents: 1,
		KnownLimits:           "Single-model server; concurrent requests serialize.",
	},
	{
		Name:         "vllm",
		Label:        "vLLM (local/self-hosted)",
		Description:  "vLLM OpenAI-compatible server on :8000. No key by default.",
		Kind:         KindOpenAICompat,
		BaseURL:      "http://localhost:8000/v1",
		NeedsKey:     false,
		Free:         true,
		Local:        true,
		DefaultModel: "default",
		RecommendedModels: []ModelSuggestion{
			{ID: "default", Label: "(whatever vLLM has loaded — see your launch command)", Free: true},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Throughput depends on model + GPU. vLLM batches well.",
	},
	{
		Name:         "together",
		Label:        "Together.ai",
		Description:  "Strong OSS model catalog (Llama, Qwen, DeepSeek, Mixtral). Free tier credits + cheap pay-as-you-go.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://api.together.xyz/v1",
		APIKeyEnv:    "TOGETHER_API_KEY",
		NeedsKey:     true,
		Free:         true,
		DefaultModel: "meta-llama/Llama-3.3-70B-Instruct-Turbo",
		RecommendedModels: []ModelSuggestion{
			{ID: "deepseek-ai/DeepSeek-V4-Pro", Label: "DeepSeek V4 Pro — top coding/reasoning ($2.10/$4.40/MTok)"},
			{ID: "Qwen/Qwen3.5-397B-A17B", Label: "Qwen3.5 397B A17B — strong all-rounder ($0.60/$3.60/MTok)"},
			{ID: "moonshotai/Kimi-K2.6", Label: "Kimi K2.6 — latest Moonshot flagship ($1.20/$4.50/MTok)"},
			{ID: "Qwen/Qwen3-235B-A22B-Instruct-2507-tput", Label: "Qwen3 235B A22B — cheap + capable ($0.20/$0.60/MTok)"},
			{ID: "Qwen/Qwen3-Coder-480B-A35B-Instruct-FP8", Label: "Qwen3 Coder 480B — code specialist ($2.00/$2.00/MTok)"},
			{ID: "deepseek-ai/DeepSeek-R1", Label: "DeepSeek R1-0528 — reasoning ($3.00/$7.00/MTok)"},
			{ID: "openai/gpt-oss-120b", Label: "GPT-OSS 120B ($0.15/$0.60/MTok)"},
			{ID: "meta-llama/Llama-3.3-70B-Instruct-Turbo", Label: "Llama 3.3 70B Turbo — proven stable ($0.88/MTok)", Free: true},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Free trial credits on signup (~$1 + more on verification). Pay-as-you-go after. Note: Llama 4 is not available serverlessly; Qwen3.5 and Kimi K2 are current flagships.",
	},
	{
		Name:         "opencode",
		Label:        "OpenCode Zen",
		Description:  "SST's OpenCode-hosted broker for free OSS models (GPT-OSS, Qwen Coder, etc.). Override OPENCODE_BASE_URL if your gateway differs.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://opencode.ai/zen/v1",
		APIKeyEnv:    "OPENCODE_API_KEY",
		NeedsKey:     true,
		Free:         true,
		DefaultModel: "qwen3-coder-480b",
		RecommendedModels: []ModelSuggestion{
			{ID: "qwen3-coder-480b", Label: "Qwen 3 Coder 480B (free)", Free: true},
			{ID: "kimi-k2", Label: "Kimi K2 (free)", Free: true},
		},
		SuggestedMaxSubagents: 2,
		KnownLimits:           "Free tier with shared upstream limits. Set max-subagents conservatively (≤2). Endpoint is the SST OpenCode Zen broker; override with OPENCODE_BASE_URL if you self-host or use a different gateway.",
	},
	{
		Name:         "ollama-cloud",
		Label:        "Ollama Cloud",
		Description:  "Ollama-hosted Turbo cloud inference. Trial credits.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://api.ollama.com/v1",
		APIKeyEnv:    "OLLAMA_API_KEY",
		NeedsKey:     true,
		Free:         true,
		DefaultModel: "llama3.3:70b",
		RecommendedModels: []ModelSuggestion{
			{ID: "llama3.3:70b", Label: "Llama 3.3 70B"},
			{ID: "qwen2.5-coder:32b", Label: "Qwen 2.5 Coder 32B"},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Trial credits on signup.",
	},
	{
		Name:                  "custom",
		Label:                 "Custom OpenAI-compatible endpoint",
		Description:           "Point at any OpenAI-compatible server (OpenCode local proxy, internal gateways, self-hosted boxes).",
		Kind:                  KindOpenAICompat,
		NeedsKey:              false,
		Free:                  false,
		DefaultModel:          "",
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Limits depend on the upstream you point at.",
	},
}

// LookupProvider returns the named provider spec or false.
func LookupProvider(name string) (ProviderSpec, bool) {
	for _, p := range providers {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderSpec{}, false
}

// AllProviders returns the catalog in display order.
func AllProviders() []ProviderSpec {
	out := make([]ProviderSpec, len(providers))
	copy(out, providers)
	return out
}
