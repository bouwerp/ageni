package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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

	MaxSubagents int
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

	return &Config{
		Master:       master,
		Subagent:     sub,
		MaxSubagents: intOr("AGENI_MAX_SUBAGENTS", 8),
	}, nil
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

// loadDotenvChain loads dotenv files in increasing-precedence order.
func loadDotenvChain() {
	// Global.
	if home, err := os.UserHomeDir(); err == nil {
		_ = godotenv.Load(filepath.Join(home, ".ageni", ".env"))
	}
	// Project.
	_ = godotenv.Load(".env")
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
