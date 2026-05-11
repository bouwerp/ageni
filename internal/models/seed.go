package models

// seedData is the static canonical model registry. Each entry provides:
//   - A stable canonical ID (provider-independent, kebab-case)
//   - Provider-specific model IDs for all known providers
//   - AiderAliases matching names seen in the Aider polyglot YAML/leaderboard
//   - An initial "seed_blended" score (0-100) based on published benchmarks
//     and released Aider polyglot results — updated by the live fetcher
//
// Tier mapping:
//
//	flagship  ≥ 70  → master/critic tier
//	mid       50-69 → sonnet sub-agent tier
//	fast      30-49 → haiku sub-agent tier
//	tiny      < 30  → local/free fallback
var seedData = []CanonicalModel{
	// ── Anthropic ────────────────────────────────────────────────────────────
	{
		ID: "claude-opus-4", Name: "Claude Opus 4", Family: "claude", Tier: "flagship",
		ProviderIDs: map[string]string{
			"anthropic":  "claude-opus-4-7",
			"openrouter": "anthropic/claude-opus-4.7",
		},
		AiderAliases: []string{"claude-opus-4-20250514", "claude-opus-4"},
		Scores:       map[string]float64{"seed_blended": 72.0},
		Notes:        "flagship; planner-grade reasoning + best tool-use compliance",
	},
	{
		ID: "claude-sonnet-4", Name: "Claude Sonnet 4", Family: "claude", Tier: "flagship",
		ProviderIDs: map[string]string{
			"anthropic":  "claude-sonnet-4-6",
			"openrouter": "anthropic/claude-sonnet-4.6",
		},
		AiderAliases: []string{"claude-sonnet-4-20250514", "claude-sonnet-4"},
		Scores:       map[string]float64{"seed_blended": 57.0},
		Notes:        "strong default; high tool-use compliance",
	},
	{
		ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", Family: "claude", Tier: "flagship",
		ProviderIDs: map[string]string{
			"anthropic":  "claude-3-7-sonnet-20250219",
			"openrouter": "anthropic/claude-3-7-sonnet",
		},
		AiderAliases: []string{"claude-3-7-sonnet-20250219", "claude-3-7-sonnet"},
		Scores:       map[string]float64{"seed_blended": 63.0},
		Notes:        "extended thinking model; strong on complex reasoning",
	},
	{
		ID: "claude-3-5-sonnet", Name: "Claude 3.5 Sonnet", Family: "claude", Tier: "mid",
		ProviderIDs: map[string]string{
			"anthropic":  "claude-3-5-sonnet-20241022",
			"openrouter": "anthropic/claude-3-5-sonnet",
		},
		AiderAliases: []string{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet"},
		Scores:       map[string]float64{"seed_blended": 52.0},
		Notes:        "previous-generation Sonnet; solid tool-use",
	},
	{
		ID: "claude-haiku-4", Name: "Claude Haiku 4.5", Family: "claude", Tier: "fast",
		ProviderIDs: map[string]string{
			"anthropic":  "claude-haiku-4-5-20251001",
			"openrouter": "anthropic/claude-haiku-4.5",
		},
		AiderAliases: []string{"claude-haiku-4-5", "claude-haiku-4"},
		Scores:       map[string]float64{"seed_blended": 42.0},
		Notes:        "cheap + fast; excellent for sub-agent legwork",
	},

	// ── OpenAI ───────────────────────────────────────────────────────────────
	{
		ID: "gpt-5", Name: "GPT-5", Family: "gpt", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openai":     "gpt-5",
			"openrouter": "openai/gpt-5",
		},
		AiderAliases: []string{"gpt-5"},
		Scores:       map[string]float64{"seed_blended": 88.0},
		Notes:        "OpenAI flagship; top Aider polyglot score",
	},
	{
		ID: "o3-pro", Name: "o3-pro", Family: "gpt", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openai":     "o3-pro",
			"openrouter": "openai/o3-pro",
		},
		AiderAliases: []string{"o3-pro"},
		Scores:       map[string]float64{"seed_blended": 84.9},
		Notes:        "deep reasoning at highest effort; expensive",
	},
	{
		ID: "o3", Name: "o3", Family: "gpt", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openai":     "o3",
			"openrouter": "openai/o3",
		},
		AiderAliases: []string{"o3"},
		Scores:       map[string]float64{"seed_blended": 81.3},
		Notes:        "strong reasoning; high effort mode recommended",
	},
	{
		ID: "o4-mini", Name: "o4-mini", Family: "gpt", Tier: "mid",
		ProviderIDs: map[string]string{
			"openai":     "o4-mini",
			"openrouter": "openai/o4-mini",
		},
		AiderAliases: []string{"o4-mini"},
		Scores:       map[string]float64{"seed_blended": 72.0},
		Notes:        "reasoning at sub-agent budget",
	},
	{
		ID: "o3-mini", Name: "o3-mini", Family: "gpt", Tier: "mid",
		ProviderIDs: map[string]string{
			"openai":     "o3-mini",
			"openrouter": "openai/o3-mini",
		},
		AiderAliases: []string{"o3-mini"},
		Scores:       map[string]float64{"seed_blended": 60.4},
		Notes:        "reasoning at sub-agent budget",
	},
	{
		ID: "gpt-4o", Name: "GPT-4o", Family: "gpt", Tier: "mid",
		ProviderIDs: map[string]string{
			"openai":     "gpt-4o",
			"openrouter": "openai/gpt-4o",
		},
		AiderAliases: []string{"gpt-4o", "gpt-4o-2024-11-20", "gpt-4o-2024-08-06"},
		Scores:       map[string]float64{"seed_blended": 42.0},
		Notes:        "all-rounder; solid tool use",
	},
	{
		ID: "gpt-4o-mini", Name: "GPT-4o mini", Family: "gpt", Tier: "fast",
		ProviderIDs: map[string]string{
			"openai":     "gpt-4o-mini",
			"openrouter": "openai/gpt-4o-mini",
		},
		AiderAliases: []string{"gpt-4o-mini", "gpt-4o-mini-2024-07-18"},
		Scores:       map[string]float64{"seed_blended": 25.0},
		Notes:        "cheap sub-agent default",
	},

	// ── Google Gemini ─────────────────────────────────────────────────────────
	{
		ID: "gemini-2-5-pro", Name: "Gemini 2.5 Pro", Family: "gemini", Tier: "flagship",
		ProviderIDs: map[string]string{
			"gemini":     "gemini-2.5-pro",
			"openrouter": "google/gemini-2.5-pro-preview",
		},
		AiderAliases: []string{
			"gemini 2.5 pro preview 06-05", "gemini 2.5 pro preview 05-06",
			"gemini 2.5 pro preview 03-25", "gemini-2.5-pro-preview",
			"gemini-2.5-pro",
		},
		Scores: map[string]float64{"seed_blended": 80.0},
		Notes:  "long context; competitive coding; generous free quota",
	},
	{
		ID: "gemini-2-5-flash", Name: "Gemini 2.5 Flash", Family: "gemini", Tier: "mid",
		ProviderIDs: map[string]string{
			"gemini":     "gemini-2.5-flash",
			"openrouter": "google/gemini-2.5-flash-preview",
		},
		AiderAliases: []string{
			"gemini-2.5-flash-preview", "gemini 2.5 flash preview",
			"gemini-2.5-flash-preview-05-20", "gemini-2.5-flash",
		},
		Scores: map[string]float64{"seed_blended": 55.1},
		Notes:  "fast, free quota; thinking mode available",
	},
	{
		ID: "gemini-2-0-flash", Name: "Gemini 2.0 Flash", Family: "gemini", Tier: "fast",
		ProviderIDs: map[string]string{
			"gemini":     "gemini-2.0-flash",
			"openrouter": "google/gemini-2.0-flash-001",
		},
		AiderAliases: []string{"gemini-2.0-flash", "gemini 2.0 flash"},
		Scores:       map[string]float64{"seed_blended": 38.0},
		Notes:        "free quota; lightweight tasks",
	},

	// ── DeepSeek ─────────────────────────────────────────────────────────────
	{
		ID: "deepseek-v3", Name: "DeepSeek V3", Family: "deepseek", Tier: "flagship",
		ProviderIDs: map[string]string{
			"deepseek":    "deepseek-chat",
			"together":    "deepseek-ai/DeepSeek-V3",
			"huggingface": "deepseek-ai/DeepSeek-V3",
			"openrouter":  "deepseek/deepseek-chat",
		},
		AiderAliases: []string{
			"deepseek-chat", "deepseek v3", "deepseek v3 (0324)",
			"deepseek-v3.2-exp (chat)", "deepseek-v3.2-exp",
		},
		Scores: map[string]float64{"seed_blended": 62.0},
		Notes:  "DeepSeek V3 Chat; very cheap; strong code",
	},
	{
		ID: "deepseek-r1", Name: "DeepSeek R1", Family: "deepseek", Tier: "flagship",
		ProviderIDs: map[string]string{
			"deepseek":    "deepseek-reasoner",
			"together":    "deepseek-ai/DeepSeek-R1",
			"huggingface": "deepseek-ai/DeepSeek-R1",
			"openrouter":  "deepseek/deepseek-r1",
		},
		AiderAliases: []string{
			"deepseek-reasoner", "deepseek r1", "deepseek r1 (0528)",
			"deepseek-v3.2-exp (reasoner)",
		},
		Scores: map[string]float64{"seed_blended": 72.0},
		Notes:  "DeepSeek R1; reasoning + code-strong; very cheap",
	},
	{
		ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", Family: "deepseek", Tier: "mid",
		ProviderIDs: map[string]string{
			"deepseek": "deepseek-v4-flash",
		},
		AiderAliases: []string{"deepseek-v4-flash"},
		Scores:       map[string]float64{"seed_blended": 60.0},
		Notes:        "fast, supports thinking mode; replaces deepseek-chat",
	},
	{
		ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Family: "deepseek", Tier: "flagship",
		ProviderIDs: map[string]string{
			"deepseek": "deepseek-v4-pro",
		},
		AiderAliases: []string{"deepseek-v4-pro"},
		Scores:       map[string]float64{"seed_blended": 74.0},
		Notes:        "premium reasoning; 75% off until May 2026",
	},

	// ── Meta Llama ────────────────────────────────────────────────────────────
	{
		ID: "llama-3-3-70b", Name: "Llama 3.3 70B", Family: "llama", Tier: "mid",
		ProviderIDs: map[string]string{
			"groq":        "llama-3.3-70b-versatile",
			"cerebras":    "llama-3.3-70b",
			"huggingface": "meta-llama/Llama-3.3-70B-Instruct",
			"together":    "meta-llama/Llama-3.3-70B-Instruct-Turbo",
			"openrouter":  "meta-llama/llama-3.3-70b-instruct",
			"ollama":      "llama3.3",
			"ollama-cloud": "llama3.3:70b",
		},
		AiderAliases: []string{
			"llama-3.3-70b", "llama 3.3 70b",
			"meta-llama/llama-3.3-70b-instruct",
		},
		Scores: map[string]float64{"seed_blended": 42.0},
		Notes:  "fast open-source all-rounder; low TTFT on Groq/Cerebras",
	},
	{
		ID: "llama-3-1-8b", Name: "Llama 3.1 8B", Family: "llama", Tier: "tiny",
		ProviderIDs: map[string]string{
			"groq":     "llama-3.1-8b-instant",
			"cerebras": "llama3.1-8b",
		},
		AiderAliases: []string{"llama-3.1-8b-instant", "llama3.1-8b"},
		Scores:       map[string]float64{"seed_blended": 18.0},
		Notes:        "tiny + fast; trivial tasks only",
	},

	// ── Qwen ─────────────────────────────────────────────────────────────────
	{
		ID: "qwen3-235b", Name: "Qwen3 235B A22B", Family: "qwen", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openrouter": "qwen/qwen3-235b-a22b",
		},
		AiderAliases: []string{"qwen3 235b a22b", "qwen3-235b-a22b"},
		Scores:       map[string]float64{"seed_blended": 59.6},
		Notes:        "Qwen3 MoE flagship; strong coding",
	},
	{
		ID: "qwen3-coder", Name: "Qwen3 Coder 480B", Family: "qwen", Tier: "mid",
		ProviderIDs: map[string]string{
			"openrouter": "qwen/qwen3-coder",
			"opencode":   "qwen3-coder-480b",
		},
		AiderAliases: []string{"qwen3-coder", "qwen3 coder"},
		Scores:       map[string]float64{"seed_blended": 55.0},
		Notes:        "code-specialized Qwen3",
	},
	{
		ID: "qwen-2-5-coder-32b", Name: "Qwen 2.5 Coder 32B", Family: "qwen", Tier: "mid",
		ProviderIDs: map[string]string{
			"huggingface": "Qwen/Qwen2.5-Coder-32B-Instruct",
			"together":    "Qwen/Qwen2.5-Coder-32B-Instruct",
			"openrouter":  "qwen/qwen-2.5-coder-32b-instruct",
			"ollama":      "qwen2.5-coder:32b",
			"ollama-cloud": "qwen2.5-coder:32b",
		},
		AiderAliases: []string{"qwen2.5-coder-32b-instruct", "qwen/qwen2.5-coder-32b-instruct"},
		Scores:       map[string]float64{"seed_blended": 45.0},
		Notes:        "code-specialized; punches above its weight",
	},

	// ── Mistral ───────────────────────────────────────────────────────────────
	{
		ID: "mistral-large", Name: "Mistral Large", Family: "mistral", Tier: "mid",
		ProviderIDs: map[string]string{
			"mistral":    "mistral-large-latest",
			"openrouter": "mistralai/mistral-large",
		},
		AiderAliases: []string{"mistral-large-latest", "mistral-large"},
		Scores:       map[string]float64{"seed_blended": 48.0},
		Notes:        "Mistral flagship; solid general purpose",
	},
	{
		ID: "codestral", Name: "Codestral", Family: "mistral", Tier: "mid",
		ProviderIDs: map[string]string{
			"mistral":    "codestral-latest",
			"openrouter": "mistralai/codestral-2501",
		},
		AiderAliases: []string{"codestral-latest", "codestral"},
		Scores:       map[string]float64{"seed_blended": 44.0},
		Notes:        "code-specialized Mistral model",
	},
	{
		ID: "mixtral-8x7b", Name: "Mixtral 8x7B", Family: "mistral", Tier: "fast",
		ProviderIDs: map[string]string{
			"together":    "mistralai/Mixtral-8x7B-Instruct-v0.1",
			"openrouter":  "mistralai/mixtral-8x7b-instruct",
			"huggingface": "mistralai/Mixtral-8x7B-Instruct-v0.1",
		},
		AiderAliases: []string{"mixtral-8x7b-instruct-v0.1", "mixtral 8x7b"},
		Scores:       map[string]float64{"seed_blended": 32.0},
		Notes:        "Mixtral MoE 8x7B; ageing but cost-effective",
	},

	// ── xAI Grok ─────────────────────────────────────────────────────────────
	{
		ID: "grok-4", Name: "Grok 4", Family: "grok", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openrouter": "x-ai/grok-4",
		},
		AiderAliases: []string{"grok-4", "grok 4"},
		Scores:       map[string]float64{"seed_blended": 79.6},
		Notes:        "xAI flagship; strong reasoning",
	},
	{
		ID: "grok-3", Name: "Grok 3", Family: "grok", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openrouter": "x-ai/grok-3-beta",
		},
		AiderAliases: []string{"grok-3-beta", "grok 3 beta", "grok-3"},
		Scores:       map[string]float64{"seed_blended": 53.3},
		Notes:        "xAI Grok 3; strong general purpose",
	},

	// ── Kimi / MoonshotAI ────────────────────────────────────────────────────
	{
		ID: "kimi-k2.6", Name: "Kimi K2.6", Family: "kimi", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openrouter": "moonshotai/kimi-k2.6",
		},
		AiderAliases: []string{"kimi k2.6", "kimi-k2.6"},
		Scores:       map[string]float64{"seed_blended": 61.0},
		Notes:        "Moonshot K2.6; latest Kimi flagship",
	},
	{
		ID: "kimi-k2", Name: "Kimi K2", Family: "kimi", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openrouter": "moonshotai/kimi-k2",
			"opencode":   "kimi-k2",
		},
		AiderAliases: []string{"kimi k2", "kimi-k2"},
		Scores:       map[string]float64{"seed_blended": 59.1},
		Notes:        "Moonshot K2; strong coding",
	},

	// ── MiniMax ───────────────────────────────────────────────────────────────
	{
		ID: "minimax-m2.7", Name: "MiniMax M2.7", Family: "minimax", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openrouter": "minimax/minimax-m2.7",
		},
		AiderAliases: []string{"minimax m2.7", "minimax-m2.7"},
		Scores:       map[string]float64{"seed_blended": 58.0},
		Notes:        "MiniMax M2.7; latest MiniMax flagship",
	},

	// ── NVIDIA Nemotron ───────────────────────────────────────────────────────
	{
		ID: "nemotron-3-super-120b", Name: "Nemotron 3 Super 120B", Family: "nemotron", Tier: "flagship",
		ProviderIDs: map[string]string{
			"openrouter": "nvidia/nemotron-3-super-120b-a12b",
		},
		AiderAliases: []string{"nemotron-3-super-120b-a12b", "nemotron 3 super"},
		Scores:       map[string]float64{"seed_blended": 62.0},
		Notes:        "NVIDIA 120B hybrid MoE (12B active); strong agentic workloads",
	},
	{
		ID: "llama-3.3-nemotron-super-49b", Name: "Llama 3.3 Nemotron Super 49B", Family: "nemotron", Tier: "mid",
		ProviderIDs: map[string]string{
			"openrouter": "nvidia/llama-3.3-nemotron-super-49b-v1.5",
		},
		AiderAliases: []string{"llama-3.3-nemotron-super-49b-v1.5", "llama 3.3 nemotron super 49b"},
		Scores:       map[string]float64{"seed_blended": 52.0},
		Notes:        "NVIDIA fine-tune of Llama 3.3 70B; agentic + RAG",
	},
	{
		ID: "nemotron-3-nano-30b", Name: "Nemotron 3 Nano 30B", Family: "nemotron", Tier: "fast",
		ProviderIDs: map[string]string{
			"openrouter": "nvidia/nemotron-3-nano-30b-a3b",
		},
		AiderAliases: []string{"nemotron-3-nano-30b-a3b", "nemotron 3 nano 30b"},
		Scores:       map[string]float64{"seed_blended": 37.0},
		Notes:        "NVIDIA 30B MoE (3B active); efficient edge/agent model",
	},
	{
		ID: "nemotron-nano-9b", Name: "Nemotron Nano 9B", Family: "nemotron", Tier: "tiny",
		ProviderIDs: map[string]string{
			"openrouter": "nvidia/nemotron-nano-9b-v2",
		},
		AiderAliases: []string{"nemotron-nano-9b-v2", "nemotron nano 9b"},
		Scores:       map[string]float64{"seed_blended": 26.0},
		Notes:        "NVIDIA 9B unified reasoning/chat; smallest Nemotron",
	},

	// ── GPT-OSS ───────────────────────────────────────────────────────────────
	{
		ID: "gpt-oss-120b", Name: "GPT-OSS 120B", Family: "gpt", Tier: "mid",
		ProviderIDs: map[string]string{
			"groq":       "openai/gpt-oss-120b",
			"openrouter": "openai/gpt-oss-120b",
			"opencode":   "gpt-oss-120b",
		},
		AiderAliases: []string{"gpt-oss-120b", "openai/gpt-oss-120b"},
		Scores:       map[string]float64{"seed_blended": 48.0},
		Notes:        "OpenAI OSS 120B; strong free-tier option",
	},

	// ── GLM ───────────────────────────────────────────────────────────────────
	{
		ID: "glm-4-5-air", Name: "GLM 4.5 Air", Family: "glm", Tier: "mid",
		ProviderIDs: map[string]string{
			"openrouter": "z-ai/glm-4.5-air",
		},
		AiderAliases: []string{"glm-4.5-air"},
		Scores:       map[string]float64{"seed_blended": 40.0},
		Notes:        "Zhipu GLM 4.5 Air; free on OpenRouter",
	},
}
