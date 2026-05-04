package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
)

type ModelConfig struct {
	Provider Provider
	Model    string
}

type Config struct {
	Master   ModelConfig
	Subagent ModelConfig

	AnthropicAPIKey string
	OpenAIAPIKey    string
	OpenAIBaseURL   string

	MaxSubagents int
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	c := &Config{
		Master: ModelConfig{
			Provider: providerOr("MASTER_PROVIDER", ProviderAnthropic),
			Model:    envOr("MASTER_MODEL", "claude-opus-4-7"),
		},
		Subagent: ModelConfig{
			Provider: providerOr("SUBAGENT_PROVIDER", ProviderAnthropic),
			Model:    envOr("SUBAGENT_MODEL", "claude-sonnet-4-6"),
		},
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		OpenAIBaseURL:   os.Getenv("OPENAI_BASE_URL"),
		MaxSubagents:    intOr("AGENI_MAX_SUBAGENTS", 8),
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	for _, mc := range []ModelConfig{c.Master, c.Subagent} {
		switch mc.Provider {
		case ProviderAnthropic:
			if c.AnthropicAPIKey == "" {
				return fmt.Errorf("ANTHROPIC_API_KEY required for provider=%s", mc.Provider)
			}
		case ProviderOpenAI:
			if c.OpenAIAPIKey == "" {
				return fmt.Errorf("OPENAI_API_KEY required for provider=%s", mc.Provider)
			}
		default:
			return fmt.Errorf("unknown provider: %q", mc.Provider)
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func providerOr(key string, def Provider) Provider {
	if v := os.Getenv(key); v != "" {
		return Provider(v)
	}
	return def
}

func intOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
