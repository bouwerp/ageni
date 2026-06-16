package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/bouwerp/ageni/internal/homedir"
	"github.com/bouwerp/ageni/internal/llm"
)

// RoleConfig is the resolved configuration for one agent role (master or
// sub-agent). It carries everything the adapter factory needs.
type RoleConfig struct {
	Provider llm.ProviderSpec
	Model    string
	BaseURL  string // resolved (preset, or override from MASTER_BASE_URL/SUBAGENT_BASE_URL)
	APIKey   string // resolved (role-specific override, then provider default)
}

type Config struct {
	Master   RoleConfig
	Subagent RoleConfig

	// MasterLead is an optional separate role used for the FIRST master
	// turn after each user message ("planning"). When set, the master
	// uses MasterLead for that initial turn and falls back to Master
	// for subsequent execution turns. This is the lead/worker pattern
	// (Goose's GOOSE_LEAD_MODEL): pay opus for the plan, claude-haiku
	// or gpt-mini for the tool execution that follows. Set
	// MASTER_LEAD_PROVIDER to enable; if unset, every master turn
	// uses Master.
	MasterLead       RoleConfig
	MasterLeadActive bool

	// Critic is the model used for soundboard reviews. When CriticActive is
	// true, the master can call soundboard() to get an adversarial critique
	// of its plan before executing significant changes. Should be a different
	// (ideally from a different provider) model than the master to provide
	// genuine independent second opinions. Set CRITIC_PROVIDER to enable.
	Critic       RoleConfig
	CriticActive bool

	// Compact is an optional cheap/fast model used exclusively for context
	// compaction (history summarisation). When CompactActive is false,
	// compaction falls back to the lead adapter (if set) then the primary.
	// Set COMPACT_PROVIDER to enable; e.g. COMPACT_PROVIDER=google/gemini-flash.
	Compact       RoleConfig
	CompactActive bool

	// Vision is an optional dedicated provider for image/vision calls
	// (view_image tool). When VisionActive is false, vision falls back to the
	// master adapter — which may not support images. Set VISION_PROVIDER to
	// use a dedicated vision-capable model, e.g. VISION_PROVIDER=openai/gpt-4o.
	// VISION_MODEL can also be set to override just the model name while
	// keeping the master provider's credentials.
	Vision       RoleConfig
	VisionActive bool

	// MasterFallbacks / SubagentFallbacks are ordered chains tried in
	// sequence when the primary fails with a retryable error
	// (429 / 5xx / timeout / network). Entries are specified in the
	// MASTER_FALLBACKS / SUBAGENT_FALLBACKS env var as a comma list of
	// "<provider>" or "<provider>/<model>" pairs. API keys are pulled
	// from the provider's standard env var (ANTHROPIC_API_KEY,
	// GROQ_API_KEY, etc.); entries whose key is missing are silently
	// dropped — fallbacks must never make the launch fail.
	MasterFallbacks   []RoleConfig
	SubagentFallbacks []RoleConfig

	// SubagentPool is an optional set of cloud providers used for
	// sub-agent spawning, enabling round-robin load balancing and
	// registry-guided best-model selection per tier.  Configured via
	// SUBAGENT_POOL (comma-separated "<provider>" or
	// "<provider>/<model>" entries, same format as SUBAGENT_FALLBACKS).
	//
	// When set, the AdapterFactory uses the pool for haiku/sonnet tiers
	// instead of the single SUBAGENT_PROVIDER, spreading load across
	// providers and consulting the rankings registry to pick the
	// highest-ROI model for each tier at spawn time.  Opus-tier tasks
	// (complex synthesis) still use the master adapter.
	//
	// NOTE: the master adapter intentionally never rotates — prompt
	// caching (Anthropic / OpenAI) makes repeated context reads ≈10×
	// cheaper; rotating providers throws away that discount.
	SubagentPool       []RoleConfig
	SubagentPoolActive bool

	MaxSubagents int
	// SubagentBudget is the default cap on tool calls per sub-agent when
	// the master doesn't override it via spawn_subagent's
	// budget_tool_calls argument. Driven by AGENI_SUBAGENT_BUDGET.
	SubagentBudget int

	// LocalFleet is a list of locally-hosted llama.cpp (or any
	// OpenAI-compatible) endpoints that can serve as sub-agent workers.
	// Configure via LLAMACPP_FLEET as a comma-separated list of
	// "baseURL|model" pairs, e.g.:
	//   LLAMACPP_FLEET=http://localhost:8080/v1|qwen2.5-coder,http://localhost:8081/v1|codestral
	// If model is omitted the entry is still valid; the adapter will use
	// an empty model string (server picks its loaded model).
	LocalFleet []LocalEndpoint

	// LocalFleetMode controls how the local fleet interacts with the
	// cloud sub-agent adapter. Driven by LLAMACPP_FLEET_MODE.
	//
	//   "full"   — all sub-agent spawns go to the local fleet (round-robin).
	//              The cloud SUBAGENT_PROVIDER is kept for the master and
	//              opus-tier tasks but all standard workers run locally.
	//   "subset" — haiku-tier spawns go to the local fleet; sonnet/opus
	//              spawns go to the cloud adapter. Useful when local hardware
	//              handles bulk grep/search/edit work while cloud handles
	//              complex reasoning turns.
	//
	// Empty string means the local fleet is inactive even if LocalFleet
	// is populated (safe default).
	LocalFleetMode string

	// SessionLogMode controls how much raw session content is persisted to
	// disk. Supported values: "private" (default) and "full".
	SessionLogMode string

	// CollaborationMode defines the default multi-LLM teamwork configuration.
	// Driven by AGENI_COLLABORATION_MODE (off, cascade, debate, self_moa).
	CollaborationMode string
}

// LocalEndpoint is one locally-hosted model server in the fleet.
type LocalEndpoint struct {
	BaseURL string // e.g. "http://localhost:8080/v1"
	Model   string // model ID passed to the server; empty = use whatever is loaded
}

// Load resolves configuration from (in order, last wins):
//
//  1. ~/.ageni/.env     (global default written by `ageni init`)
//  2. ./.env            (per-project override)
//  3. real environment  (always wins)
//
// Returns nil, nil with err==ErrNotConfigured if no provider is set anywhere
// — the caller should drop into the wizard in that case.
func Load() (*Config, error) {
	loadDotenvChain()

	masterRaw := os.Getenv("MASTER_PROVIDER")
	subRaw := os.Getenv("SUBAGENT_PROVIDER")
	if masterRaw == "" && subRaw == "" {
		return nil, ErrNotConfigured
	}
	if masterRaw == "" {
		return nil, fmt.Errorf("MASTER_PROVIDER not set")
	}
	if subRaw == "" {
		return nil, fmt.Errorf("SUBAGENT_PROVIDER not set")
	}

	master, err := resolveRole("MASTER", masterRaw)
	if err != nil {
		return nil, fmt.Errorf("master: %w", err)
	}
	sub, err := resolveRole("SUBAGENT", subRaw)
	if err != nil {
		return nil, fmt.Errorf("sub-agent: %w", err)
	}

	cfg := &Config{
		Master:         master,
		Subagent:       sub,
		MaxSubagents:   intOr("AGENI_MAX_SUBAGENTS", 8),
		SubagentBudget:    intOr("AGENI_SUBAGENT_BUDGET", 200),
		SessionLogMode:    defaultSessionLogMode(os.Getenv("AGENI_SESSION_LOG_MODE")),
		CollaborationMode: strings.TrimSpace(strings.ToLower(os.Getenv("AGENI_COLLABORATION_MODE"))),
	}

	// MasterLead is opt-in: only resolve if MASTER_LEAD_PROVIDER is set.
	// On any resolution error we silently disable lead routing and fall
	// back to Master — a misconfigured lead must never refuse to launch.
	if leadRaw := os.Getenv("MASTER_LEAD_PROVIDER"); leadRaw != "" {
		if lead, err := resolveRole("MASTER_LEAD", leadRaw); err == nil {
			cfg.MasterLead = lead
			cfg.MasterLeadActive = true
		}
	}

	// Critic is opt-in: only resolve if CRITIC_PROVIDER is set.
	if criticRaw := os.Getenv("CRITIC_PROVIDER"); criticRaw != "" {
		if critic, err := resolveRole("CRITIC", criticRaw); err == nil {
			cfg.Critic = critic
			cfg.CriticActive = true
		}
	}

	// Compact is opt-in: only resolve if COMPACT_PROVIDER is set.
	if compactRaw := os.Getenv("COMPACT_PROVIDER"); compactRaw != "" {
		if compact, err := resolveRole("COMPACT", compactRaw); err == nil {
			cfg.Compact = compact
			cfg.CompactActive = true
		}
	}

	// Vision is opt-in: only resolve if VISION_PROVIDER is set.
	if visionRaw := os.Getenv("VISION_PROVIDER"); visionRaw != "" {
		if vision, err := resolveRole("VISION", visionRaw); err == nil {
			cfg.Vision = vision
			cfg.VisionActive = true
		}
	}

	cfg.MasterFallbacks = parseFallbacks(os.Getenv("MASTER_FALLBACKS"))
	cfg.SubagentFallbacks = parseFallbacks(os.Getenv("SUBAGENT_FALLBACKS"))

	// SubagentPool is opt-in; parse errors silently yield an empty pool.
	if pool := parseFallbacks(os.Getenv("SUBAGENT_POOL")); len(pool) > 0 {
		cfg.SubagentPool = pool
		cfg.SubagentPoolActive = true
	}

	// Local fleet is opt-in — a parse error silently yields an empty fleet.
	cfg.LocalFleet = parseLocalFleet(os.Getenv("LLAMACPP_FLEET"))
	if mode := strings.TrimSpace(os.Getenv("LLAMACPP_FLEET_MODE")); mode == "full" || mode == "subset" {
		cfg.LocalFleetMode = mode
	}
	return cfg, nil
}

// parseFallbacks reads a comma-separated list of "<provider>" or
// "<provider>/<model>" entries and resolves each into a RoleConfig.
// API keys are pulled from the provider's standard env var. Entries
// that can't be authenticated or whose provider is unknown are
// silently dropped — a misconfigured fallback must NEVER block the
// launch, since the primary still works.
func parseFallbacks(spec string) []RoleConfig {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	var out []RoleConfig
	for _, raw := range strings.Split(spec, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		provName, modelOverride := entry, ""
		if idx := strings.IndexByte(entry, '/'); idx > 0 {
			provName = entry[:idx]
			modelOverride = entry[idx+1:]
		}
		spec, ok := llm.LookupProvider(provName)
		if !ok {
			continue
		}
		rc := RoleConfig{Provider: spec, BaseURL: spec.BaseURL}
		rc.Model = modelOverride
		if rc.Model == "" {
			rc.Model = spec.DefaultModel
		}
		if rc.Model == "" {
			continue
		}
		if spec.NeedsKey {
			if spec.APIKeyEnv != "" {
				rc.APIKey = os.Getenv(spec.APIKeyEnv)
			}
			if rc.APIKey == "" {
				continue // can't auth — drop silently
			}
		}
		out = append(out, rc)
	}
	return out
}

// parseLocalFleet reads a comma-separated list of "baseURL|model" entries.
// Each entry must have at least a baseURL; model is optional.
// Malformed or empty entries are silently skipped.
// Example: "http://localhost:8080/v1|qwen2.5-coder,http://localhost:8081/v1|codestral"
func parseLocalFleet(spec string) []LocalEndpoint {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	var out []LocalEndpoint
	for _, raw := range strings.Split(spec, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		baseURL, model := entry, ""
		if idx := strings.LastIndex(entry, "|"); idx > 0 {
			baseURL = strings.TrimSpace(entry[:idx])
			model = strings.TrimSpace(entry[idx+1:])
		}
		if baseURL == "" {
			continue
		}
		out = append(out, LocalEndpoint{BaseURL: baseURL, Model: model})
	}
	return out
}

// FormatLocalFleet serialises a fleet slice back to the env var format.
func FormatLocalFleet(fleet []LocalEndpoint) string {
	parts := make([]string, 0, len(fleet))
	for _, e := range fleet {
		if e.Model != "" {
			parts = append(parts, e.BaseURL+"|"+e.Model)
		} else {
			parts = append(parts, e.BaseURL)
		}
	}
	return strings.Join(parts, ",")
}

var ErrNotConfigured = fmt.Errorf("ageni is not configured; run `ageni init`")

func defaultSessionLogMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "full":
		return "full"
	default:
		return "private"
	}
}

func resolveRole(prefix, providerName string) (RoleConfig, error) {
	spec, ok := llm.LookupProvider(providerName)
	if !ok {
		return RoleConfig{}, fmt.Errorf("unknown provider %q (run `ageni init` to see the list)", providerName)
	}

	rc := RoleConfig{Provider: spec}

	rc.Model = os.Getenv(prefix + "_MODEL")
	if rc.Model == "" {
		rc.Model = spec.DefaultModel
	}
	if rc.Model == "" {
		return rc, fmt.Errorf("%s_MODEL is required for provider %q", prefix, providerName)
	}

	rc.BaseURL = os.Getenv(prefix + "_BASE_URL")
	if rc.BaseURL == "" {
		rc.BaseURL = spec.BaseURL
	}
	if spec.Kind == llm.KindOpenAICompat && rc.BaseURL == "" {
		return rc, fmt.Errorf("%s_BASE_URL is required for provider %q", prefix, providerName)
	}

	if spec.NeedsKey {
		rc.APIKey = os.Getenv(prefix + "_API_KEY")
		if rc.APIKey == "" && spec.APIKeyEnv != "" {
			rc.APIKey = os.Getenv(spec.APIKeyEnv)
		}
		if rc.APIKey == "" {
			env := spec.APIKeyEnv
			if env == "" {
				env = prefix + "_API_KEY"
			}
			return rc, fmt.Errorf("API key required: set %s or %s_API_KEY", env, prefix)
		}
	} else {
		// Optional auth (e.g. some custom endpoints). Pull anyway if set.
		if v := os.Getenv(prefix + "_API_KEY"); v != "" {
			rc.APIKey = v
		}
	}

	return rc, nil
}

// loadDotenvChain loads dotenv files into the process env. godotenv.Load is
// non-overwriting (first to set wins), so we load in precedence order:
//  1. Real env (already in os.Environ — wins automatically)
//  2. ./.env (project)
//  3. ~/.ageni/.env (global default)
func loadDotenvChain() {
	_ = godotenv.Load(".env")
	if home, err := homedir.Dir(); err == nil {
		_ = godotenv.Load(filepath.Join(home, ".ageni", ".env"))
	}
}

// GlobalEnvPath returns the standard location for the user-level config.
func GlobalEnvPath() (string, error) {
	home, err := homedir.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ageni", ".env"), nil
}

func intOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
