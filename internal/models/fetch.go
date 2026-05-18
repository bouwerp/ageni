package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	aiderYAMLURL    = "https://raw.githubusercontent.com/paul-gauthier/aider/main/aider/website/_data/polyglot_leaderboard.yml"
	openRouterURL   = "https://openrouter.ai/api/v1/models"
	fetchTimeout    = 20 * time.Second
)

// FetchAiderScores downloads the Aider polyglot leaderboard YAML from GitHub
// and returns a map of normalised model name → pass_rate_2 score (0-100).
// It keeps only the highest score when the same model appears multiple times
// (e.g. different effort levels).
func FetchAiderScores(ctx context.Context) (map[string]float64, error) {
	body, err := get(ctx, aiderYAMLURL)
	if err != nil {
		return nil, fmt.Errorf("aider leaderboard: %w", err)
	}
	return parseAiderYAML(body), nil
}

// parseAiderYAML extracts model → pass_rate_2 pairs from the Aider polyglot
// YAML without importing a YAML library. The file is a simple list of maps
// with consistent key ordering; a line-based scan is sufficient.
func parseAiderYAML(data []byte) map[string]float64 {
	result := make(map[string]float64)
	var currentModel string

	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "model:") {
			val := strings.TrimSpace(line[len("model:"):])
			val = strings.Trim(val, `"'`)
			currentModel = val
		} else if strings.HasPrefix(line, "pass_rate_2:") && currentModel != "" {
			val := strings.TrimSpace(line[len("pass_rate_2:"):])
			score, err := strconv.ParseFloat(val, 64)
			if err == nil && score >= 0 {
				norm := normaliseAiderName(currentModel)
				if existing, ok := result[norm]; !ok || score > existing {
					result[norm] = score
				}
			}
			// Also store the raw model name for alias resolution.
			if score, err2 := strconv.ParseFloat(val, 64); err2 == nil {
				raw := normaliseAiderName(currentModel)
				if raw != currentModel {
					if existing, ok := result[currentModel]; !ok || score > existing {
						result[currentModel] = score
					}
				}
			}
			currentModel = ""
		} else if strings.HasPrefix(line, "- dirname:") {
			// New entry block — reset current model.
			currentModel = ""
		}
	}
	return result
}

// openRouterPricing holds the raw per-token prices returned by OpenRouter.
type openRouterPricing struct {
	Prompt            string `json:"prompt"`
	Completion        string `json:"completion"`
	InternalReasoning string `json:"internal_reasoning"`
}

// openRouterTopProvider holds per-provider constraints.
type openRouterTopProvider struct {
	MaxCompletionTokens int `json:"max_completion_tokens"`
}

// openRouterArchitecture holds the model architecture metadata from OpenRouter.
type openRouterArchitecture struct {
	InputModalities []string `json:"input_modalities"`
}

// openRouterModel is the minimal subset of the OpenRouter /models JSON we care about.
type openRouterModel struct {
	ID                  string                `json:"id"`
	ContextLength       int                   `json:"context_length"`
	Pricing             openRouterPricing     `json:"pricing"`
	Architecture        openRouterArchitecture `json:"architecture"`
	TopProvider         openRouterTopProvider  `json:"top_provider"`
	SupportedParameters []string              `json:"supported_parameters"`
}

type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

// FetchOpenRouterAvailability queries OpenRouter for the full model list and
// returns:
//   - avail: set of "openrouter:modelID" keys for currently available models
//   - costs: "openrouter:modelID" → [inputPerM, outputPerM] in USD
//   - caps: "openrouter:modelID" → []string capability names (e.g. ["vision"])
//   - windows: "openrouter:modelID" → [contextWindow, maxOutputTokens] in tokens
//   - thinking: "openrouter:modelID" → thinking token cost per million USD
//   - params: "openrouter:modelID" → []string supported_parameters
//
// No API key is required for the public listing endpoint.
func FetchOpenRouterAvailability(ctx context.Context) (
	avail map[string]bool,
	costs map[string][2]float64,
	caps map[string][]string,
	windows map[string][2]int,
	thinking map[string]float64,
	params map[string][]string,
	err error,
) {
	body, err := get(ctx, openRouterURL)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("openrouter models: %w", err)
	}
	var resp openRouterResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("openrouter models: json: %w", err)
	}
	n := len(resp.Data)
	avail = make(map[string]bool, n)
	costs = make(map[string][2]float64, n)
	caps = make(map[string][]string, n)
	windows = make(map[string][2]int, n)
	thinking = make(map[string]float64, n)
	params = make(map[string][]string, n)

	for _, m := range resp.Data {
		if m.ID == "" {
			continue
		}
		key := "openrouter:" + m.ID
		avail[key] = true

		// Pricing: input and output costs.
		inp, e1 := strconv.ParseFloat(m.Pricing.Prompt, 64)
		out, e2 := strconv.ParseFloat(m.Pricing.Completion, 64)
		if e1 == nil && e2 == nil {
			costs[key] = [2]float64{inp * 1e6, out * 1e6}
		}

		// Thinking token cost (separate billing).
		if tc, e3 := strconv.ParseFloat(m.Pricing.InternalReasoning, 64); e3 == nil && tc > 0 {
			thinking[key] = tc * 1e6
		}

		// Context window and max output tokens.
		maxOut := m.TopProvider.MaxCompletionTokens
		if m.ContextLength > 0 || maxOut > 0 {
			windows[key] = [2]int{m.ContextLength, maxOut}
		}

		// Detect vision capability from input_modalities.
		var modelCaps []string
		for _, mod := range m.Architecture.InputModalities {
			if mod == "image" {
				modelCaps = append(modelCaps, "vision")
				break
			}
		}
		if len(modelCaps) > 0 {
			caps[key] = modelCaps
		}

		// Supported parameters for capability derivation.
		if len(m.SupportedParameters) > 0 {
			params[key] = m.SupportedParameters
		}
	}
	return avail, costs, caps, windows, thinking, params, nil
}

// get performs a GET request with the given context and returns the body.
func get(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ageni/1.0 model-ranking-fetcher")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
}
