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
		DefaultModel: "gpt-4o",
		RecommendedModels: []ModelSuggestion{
			{ID: "gpt-4o", Label: "GPT-4o"},
			{ID: "gpt-4o-mini", Label: "GPT-4o mini — cheap sub-agent default"},
			{ID: "o3-mini", Label: "o3-mini — reasoning"},
		},
		SuggestedMaxSubagents: 8,
		SupportsCaching:       true,
		KnownLimits:           "Pay-as-you-go. Tier-based RPM.",
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
			{ID: "openai/gpt-4o", Label: "GPT-4o (paid)"},
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
			{ID: "llama-3.1-8b-instant", Label: "Llama 3.1 8B Instant (free, fast)", Free: true},
			{ID: "openai/gpt-oss-120b", Label: "GPT-OSS 120B (free)", Free: true},
			{ID: "openai/gpt-oss-20b", Label: "GPT-OSS 20B (free)", Free: true},
		},
		SuggestedMaxSubagents: 2,
		KnownLimits:           "Free tier: ~30 requests/min. Set max-subagents=2 to avoid 429s.",
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
		DefaultModel: "llama-3.3-70b",
		RecommendedModels: []ModelSuggestion{
			{ID: "llama-3.3-70b", Label: "Llama 3.3 70B (free)", Free: true},
			{ID: "llama3.1-8b", Label: "Llama 3.1 8B (free)", Free: true},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Free tier: ~30 RPM, 1M tokens/day. Very fast TTFT. Verify model availability at inference-docs.cerebras.ai/llms.txt.",
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
		DefaultModel: "mistral-large-latest",
		RecommendedModels: []ModelSuggestion{
			{ID: "mistral-large-latest", Label: "Mistral Large", Free: true},
			{ID: "codestral-latest", Label: "Codestral — code-specialized", Free: true},
			{ID: "open-mistral-nemo", Label: "Mistral Nemo", Free: true},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Free tier: 1 RPS, 500k tokens/min. Opt in via dashboard.",
	},
	{
		Name:         "deepseek",
		Label:        "DeepSeek",
		Description:  "DeepSeek V3 / R1 from the source. Trial credits + cheap pay-as-you-go.",
		Kind:         KindOpenAICompat,
		BaseURL:      "https://api.deepseek.com/v1",
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		NeedsKey:     true,
		Free:         false,
		DefaultModel: "deepseek-chat",
		RecommendedModels: []ModelSuggestion{
			{ID: "deepseek-chat", Label: "DeepSeek V3"},
			{ID: "deepseek-reasoner", Label: "DeepSeek R1 — reasoning"},
		},
		SuggestedMaxSubagents: 8,
		KnownLimits:           "Trial credits on signup; pay-as-you-go after. Very cheap.",
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
			{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro (free quota)", Free: true},
			{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash (free quota)", Free: true},
			{ID: "gemini-2.0-flash", Label: "Gemini 2.0 Flash (free quota)", Free: true},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Free tier varies by model; Pro has stricter quotas than Flash.",
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
			{ID: "meta-llama/Llama-3.3-70B-Instruct-Turbo", Label: "Llama 3.3 70B Turbo", Free: true},
			{ID: "Qwen/Qwen2.5-Coder-32B-Instruct", Label: "Qwen 2.5 Coder 32B (code-specialized)", Free: true},
			{ID: "deepseek-ai/DeepSeek-V3", Label: "DeepSeek V3"},
			{ID: "deepseek-ai/DeepSeek-R1", Label: "DeepSeek R1 — reasoning"},
			{ID: "mistralai/Mixtral-8x7B-Instruct-v0.1", Label: "Mixtral 8x7B", Free: true},
			{ID: "Qwen/Qwen2.5-72B-Instruct-Turbo", Label: "Qwen 2.5 72B Turbo", Free: true},
		},
		SuggestedMaxSubagents: 4,
		KnownLimits:           "Free tier credits ($1 on signup, more for verification). Pay-as-you-go after; competitive prices on OSS models.",
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
