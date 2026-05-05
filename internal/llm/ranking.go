package llm

import "strings"

// ModelRanking is a hand-curated 0-100 score blending public coding-agent
// benchmarks (Aider polyglot leaderboard, Artificial Analysis composite,
// SWE-bench results) plus tool-use compliance signals from real ageni
// runs. Higher = better for agent workloads (planning, tool calls,
// multi-file edits). Updated periodically; the LastUpdated string makes
// staleness visible to anyone reading the table.
//
// Why a hand-curated table:
//   - There is no canonical cross-vendor benchmark every model has run.
//   - Aider's polyglot covers most flagships but misses many local /
//     OpenRouter models.
//   - We need a single number to render in the settings model picker,
//     so users can pick a tier-appropriate model at a glance.
//
// When a model isn't in the table, callers see Score=0 and should hide
// the rank label rather than show "0/100".
type ModelRanking struct {
	Score       int    // 0-100
	Notes       string // one-line context ("strong reasoning", "code-specialized", etc.)
	LastUpdated string // YYYY-MM-DD; helps spot stale entries
}

// modelRankings is the canonical table. Lookups normalise the model
// string (strip provider prefix, strip :free suffix) before checking,
// so "anthropic/claude-opus-4-7" and "claude-opus-4-7" both find this
// entry, and "meta-llama/llama-3.3-70b-instruct:free" finds the
// non-free Llama 3.3 entry.
var modelRankings = map[string]ModelRanking{
	// Anthropic
	"claude-opus-4-7":            {Score: 90, Notes: "flagship; planner-grade reasoning + best tool-use compliance", LastUpdated: "2026-05"},
	"claude-sonnet-4-6":          {Score: 84, Notes: "strong default; high tool-use compliance", LastUpdated: "2026-05"},
	"claude-haiku-4-5-20251001":  {Score: 65, Notes: "cheap, fast; excellent for sub-agent legwork", LastUpdated: "2026-05"},
	"claude-3-5-sonnet-20241022": {Score: 78, Notes: "previous generation Sonnet", LastUpdated: "2026-05"},
	// OpenAI
	"gpt-5":          {Score: 88, Notes: "flagship reasoning; udiff-format edits", LastUpdated: "2026-05"},
	"gpt-4o":         {Score: 75, Notes: "all-rounder; solid tool use", LastUpdated: "2026-05"},
	"gpt-4o-mini":    {Score: 55, Notes: "cheap sub-agent default", LastUpdated: "2026-05"},
	"o3":             {Score: 82, Notes: "deep reasoning; slow", LastUpdated: "2026-05"},
	"o3-mini":        {Score: 70, Notes: "reasoning at sub-agent budget", LastUpdated: "2026-05"},
	"o4-mini":        {Score: 73, Notes: "reasoning at sub-agent budget", LastUpdated: "2026-05"},
	// Google
	"gemini-2.5-pro":   {Score: 80, Notes: "long context; competitive coding", LastUpdated: "2026-05"},
	"gemini-2.5-flash": {Score: 62, Notes: "fast, free quota", LastUpdated: "2026-05"},
	"gemini-2.0-flash": {Score: 55, Notes: "free quota", LastUpdated: "2026-05"},
	// DeepSeek
	"deepseek-chat":     {Score: 72, Notes: "DeepSeek V3; very cheap", LastUpdated: "2026-05"},
	"deepseek-reasoner": {Score: 76, Notes: "DeepSeek R1; reasoning, code-strong", LastUpdated: "2026-05"},
	"deepseek-ai/DeepSeek-V3": {Score: 72, Notes: "via Together / HF", LastUpdated: "2026-05"},
	"deepseek-ai/DeepSeek-R1": {Score: 76, Notes: "reasoning, code-strong", LastUpdated: "2026-05"},
	// Meta Llama
	"llama-3.3-70b-versatile":               {Score: 50, Notes: "Groq's Llama 3.3 70B — fast TTFT", LastUpdated: "2026-05"},
	"llama-3.3-70b":                         {Score: 50, Notes: "Cerebras Llama 3.3 70B", LastUpdated: "2026-05"},
	"meta-llama/Llama-3.3-70B-Instruct":     {Score: 50, Notes: "tool-use compliance varies by host", LastUpdated: "2026-05"},
	"meta-llama/Llama-3.3-70B-Instruct-Turbo": {Score: 52, Notes: "Together turbo variant", LastUpdated: "2026-05"},
	"meta-llama/llama-3.3-70b-instruct":     {Score: 50, Notes: "OpenRouter routing", LastUpdated: "2026-05"},
	"llama3.3":                              {Score: 50, Notes: "local Ollama tag", LastUpdated: "2026-05"},
	"llama-3.1-8b-instant":                  {Score: 30, Notes: "tiny + fast; trivial tasks only", LastUpdated: "2026-05"},
	"llama3.1-8b":                           {Score: 30, Notes: "tiny + fast", LastUpdated: "2026-05"},
	// Qwen
	"Qwen/Qwen2.5-Coder-32B-Instruct": {Score: 58, Notes: "code-specialized; punches above its weight", LastUpdated: "2026-05"},
	"qwen2.5-coder:32b":               {Score: 58, Notes: "code-specialized (Ollama tag)", LastUpdated: "2026-05"},
	"qwen/qwen3-coder":                {Score: 62, Notes: "Qwen 3 Coder", LastUpdated: "2026-05"},
	"qwen3-coder-480b":                {Score: 68, Notes: "Qwen 3 Coder 480B (OpenCode)", LastUpdated: "2026-05"},
	"Qwen/Qwen2.5-72B-Instruct-Turbo": {Score: 56, Notes: "general Qwen, Together turbo", LastUpdated: "2026-05"},
	// Mistral
	"mistral-large-latest":              {Score: 65, Notes: "Mistral Large", LastUpdated: "2026-05"},
	"codestral-latest":                  {Score: 60, Notes: "code-specialized", LastUpdated: "2026-05"},
	"open-mistral-nemo":                 {Score: 42, Notes: "Nemo small, fast", LastUpdated: "2026-05"},
	"mistralai/Mixtral-8x7B-Instruct-v0.1": {Score: 45, Notes: "Mixtral 8x7B; ageing but solid", LastUpdated: "2026-05"},
	// GPT-OSS
	"openai/gpt-oss-120b": {Score: 60, Notes: "OSS GPT-class; via Groq/OpenRouter/OpenCode", LastUpdated: "2026-05"},
	"gpt-oss-120b":        {Score: 60, Notes: "OSS GPT-class", LastUpdated: "2026-05"},
	"openai/gpt-oss-20b":  {Score: 45, Notes: "smaller OSS variant", LastUpdated: "2026-05"},
	"gpt-oss-20b":         {Score: 45, Notes: "smaller OSS variant", LastUpdated: "2026-05"},
	// GLM / Kimi / etc
	"z-ai/glm-4.5-air": {Score: 50, Notes: "GLM 4.5 Air", LastUpdated: "2026-05"},
	"kimi-k2":          {Score: 55, Notes: "Kimi K2", LastUpdated: "2026-05"},
}

// RankingFor returns the ranking entry for a model ID, normalising the
// lookup against ":free" suffixes and provider prefixes. Returns
// (zero, false) when no entry matches.
func RankingFor(model string) (ModelRanking, bool) {
	if model == "" {
		return ModelRanking{}, false
	}
	if r, ok := modelRankings[model]; ok {
		return r, true
	}
	// Strip ":free" suffix (OpenRouter convention).
	if strings.HasSuffix(model, ":free") {
		base := strings.TrimSuffix(model, ":free")
		if r, ok := modelRankings[base]; ok {
			return r, true
		}
	}
	// Strip provider prefix.
	if i := strings.Index(model, "/"); i > 0 {
		base := model[i+1:]
		if r, ok := modelRankings[base]; ok {
			return r, true
		}
		// And ":free" on the suffix-stripped form.
		if strings.HasSuffix(base, ":free") {
			base = strings.TrimSuffix(base, ":free")
			if r, ok := modelRankings[base]; ok {
				return r, true
			}
		}
	}
	return ModelRanking{}, false
}
