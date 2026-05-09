package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

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

	MaxSubagents int
	// SubagentBudget is the default cap on tool calls per sub-agent when
	// the master doesn't override it via spawn_subagent's
	// budget_tool_calls argument. Driven by AGENI_SUBAGENT_BUDGET.
	SubagentBudget int
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
		SubagentBudget: intOr("AGENI_SUBAGENT_BUDGET", 40),
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

	cfg.MasterFallbacks = parseFallbacks(os.Getenv("MASTER_FALLBACKS"))
	cfg.SubagentFallbacks = parseFallbacks(os.Getenv("SUBAGENT_FALLBACKS"))
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

// ErrNotConfigured indicates no provider has been chosen anywhere — caller
// should run the wizard.
var ErrNotConfigured = fmt.Errorf("ageni is not configured; run `ageni init`")

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
	if home, err := os.UserHomeDir(); err == nil {
		_ = godotenv.Load(filepath.Join(home, ".ageni", ".env"))
	}
}

// GlobalEnvPath returns the standard location for the user-level config.
func GlobalEnvPath() (string, error) {
	home, err := os.UserHomeDir()
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
