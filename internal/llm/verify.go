package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VerifyKey makes a minimal authenticated request against the provider
// and reports whether the supplied apiKey is accepted. Returns nil on
// success; an error with a short, user-facing message otherwise.
//
//   - Anthropic: HEAD /v1/models with x-api-key.
//   - OpenAI-compat: GET /v1/models with Authorization: Bearer.
//   - Local providers (Ollama, llama.cpp, vLLM): no auth needed; the
//     base URL is probed instead.
//   - "custom": no verification possible without knowing the URL —
//     returns a "skipped" success.
//
// Capped at 5 seconds. Any 2xx is treated as success; a 401/403 is the
// only definitive failure. Other errors (network, timeout, 5xx) return
// "couldn't reach provider" so the user knows verification was
// inconclusive rather than a wrong key.
func VerifyKey(ctx context.Context, spec ProviderSpec, apiKey string) error {
	if spec.Name == "custom" {
		return nil
	}
	timeout := 5 * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch spec.Kind {
	case KindAnthropic:
		return verifyAnthropic(ctx, apiKey)
	case KindOllamaCloud:
		return verifyOllamaCloud(ctx, spec, apiKey)
	default:
		return verifyOpenAICompat(ctx, spec, apiKey)
	}
}

// verifyAnthropic hits the Anthropic /v1/models endpoint. Returns nil
// for any 2xx; "invalid api key" for 401/403; "couldn't reach …" for
// anything else.
func verifyAnthropic(ctx context.Context, apiKey string) error {
	if apiKey == "" {
		return errors.New("no API key set")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models?limit=1", nil)
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach api.anthropic.com: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return errors.New("invalid API key (401/403)")
	default:
		return fmt.Errorf("HTTP %d from api.anthropic.com — verification inconclusive", resp.StatusCode)
	}
}

// verifyOllamaCloud hits the native Ollama Cloud /api/tags endpoint with
// Bearer auth to confirm the key is accepted.
func verifyOllamaCloud(ctx context.Context, spec ProviderSpec, apiKey string) error {
	if apiKey == "" {
		return errors.New("no API key set")
	}
	base := strings.TrimRight(spec.BaseURL, "/")
	if base == "" {
		base = "https://ollama.com"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach %s: %w", base, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return errors.New("invalid API key (401/403)")
	default:
		return fmt.Errorf("HTTP %d from %s — verification inconclusive", resp.StatusCode, base)
	}
}

// verifyOpenAICompat hits {BaseURL}/models with bearer auth (or no auth
// for local providers).
func verifyOpenAICompat(ctx context.Context, spec ProviderSpec, apiKey string) error {
	base := strings.TrimRight(spec.BaseURL, "/")
	if base == "" {
		return errors.New("no base URL configured for this provider")
	}
	if spec.NeedsKey && apiKey == "" {
		return errors.New("no API key set")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach %s: %w", base, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return errors.New("invalid API key (401/403)")
	default:
		return fmt.Errorf("HTTP %d from %s — verification inconclusive", resp.StatusCode, base)
	}
}
