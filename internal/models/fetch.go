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
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

// openRouterArchitecture holds the model architecture metadata from OpenRouter.
type openRouterArchitecture struct {
	InputModalities []string `json:"input_modalities"`
}

// openRouterModel is the minimal subset of the OpenRouter /models JSON we care about.
type openRouterModel struct {
	ID           string                 `json:"id"`
	Pricing      openRouterPricing      `json:"pricing"`
	Architecture openRouterArchitecture `json:"architecture"`
}

type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

// FetchOpenRouterAvailability queries OpenRouter for the full model list and
// returns a set of "openrouter:modelID" keys representing currently available
// models, a map of "openrouter:modelID" → [inputPerM, outputPerM] costs, and
// a map of "openrouter:modelID" → []string capabilities (e.g. ["vision"]).
// No API key is required for the public listing endpoint.
func FetchOpenRouterAvailability(ctx context.Context) (avail map[string]bool, costs map[string][2]float64, caps map[string][]string, err error) {
	body, err := get(ctx, openRouterURL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("openrouter models: %w", err)
	}
	var resp openRouterResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, nil, fmt.Errorf("openrouter models: json: %w", err)
	}
	avail = make(map[string]bool, len(resp.Data))
	costs = make(map[string][2]float64, len(resp.Data))
	caps = make(map[string][]string, len(resp.Data))
	for _, m := range resp.Data {
		if m.ID == "" {
			continue
		}
		key := "openrouter:" + m.ID
		avail[key] = true
		inp, e1 := strconv.ParseFloat(m.Pricing.Prompt, 64)
		out, e2 := strconv.ParseFloat(m.Pricing.Completion, 64)
		if e1 == nil && e2 == nil {
			costs[key] = [2]float64{inp * 1e6, out * 1e6}
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
	}
	return avail, costs, caps, nil
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
