package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// parsePerToken parses an OpenRouter-style per-token rate string like
// "0.000003". Returns 0 on empty / unparsable input.
func parsePerToken(s string) (float64, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

// FetchModels queries a provider's /models endpoint (OpenAI-compatible
// shape) and returns the canonical IDs as ModelSuggestions. apiKey may be
// empty for providers that publish their model list publicly (OpenRouter).
//
// Anthropic is special-cased — its /v1/models endpoint exists but returns a
// different schema; for now we return the curated list.
func FetchModels(ctx context.Context, spec ProviderSpec, apiKey string) ([]ModelSuggestion, error) {
	if spec.Kind == KindAnthropic || spec.Kind == KindOllamaCloud {
		// These providers either have a different /models schema or don't
		// expose a standard models list; return the curated suggestions.
		return spec.RecommendedModels, nil
	}
	if spec.BaseURL == "" {
		return nil, fmt.Errorf("no base URL for provider %s", spec.Name)
	}

	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	url := strings.TrimRight(spec.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	// OpenRouter requires a Referer header for some plans; harmless
	// elsewhere.
	req.Header.Set("HTTP-Referer", "https://github.com/bouwerp/ageni")
	req.Header.Set("X-Title", "ageni")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("models endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}

	// Most providers return either {"data":[{"id":"..."}]} or
	// {"models":[{"name":"..."}]}. Try both.
	var openaiShape struct {
		Data []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
			ContextLength int    `json:"context_length"`
			Description   string `json:"description"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &openaiShape); err == nil && len(openaiShape.Data) > 0 {
		out := make([]ModelSuggestion, 0, len(openaiShape.Data))
		for _, m := range openaiShape.Data {
			id := m.ID
			if id == "" {
				continue
			}
			label := id
			if m.Name != "" && m.Name != id {
				label = m.Name + " — " + id
			}
			free := false
			if strings.Contains(strings.ToLower(id), ":free") {
				free = true
			}
			if m.Pricing.Prompt == "0" && m.Pricing.Completion == "0" {
				free = true
			}
			// Register pricing into the cost estimator. OpenRouter's API
			// returns rates as USD-per-token strings ("0.000003"), so we
			// scale to per-1M to match our Pricing struct's units.
			if pp, parseErr := parsePerToken(m.Pricing.Prompt); parseErr == nil {
				cp, _ := parsePerToken(m.Pricing.Completion)
				RegisterDynamicPricing(id, Pricing{
					InputPer1M:  pp * 1_000_000,
					OutputPer1M: cp * 1_000_000,
				})
			}
			out = append(out, ModelSuggestion{ID: id, Label: label, Free: free})
		}
		return out, nil
	}

	// Ollama-style fallback.
	var ollamaShape struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &ollamaShape); err == nil && len(ollamaShape.Models) > 0 {
		out := make([]ModelSuggestion, 0, len(ollamaShape.Models))
		for _, m := range ollamaShape.Models {
			out = append(out, ModelSuggestion{ID: m.Name, Label: m.Name, Free: true})
		}
		return out, nil
	}

	return nil, fmt.Errorf("unexpected /models response shape from %s", spec.Name)
}

// MergeModels combines curated + live model lists, deduping by ID. Curated
// entries appear first (so the wizard's defaults are easy to find); free
// models come second; the rest follow alphabetically. Returned slice is
// suitable to drop straight into a huh.Select with Filtering(true).
func MergeModels(curated, live []ModelSuggestion) []ModelSuggestion {
	seen := map[string]bool{}
	out := make([]ModelSuggestion, 0, len(curated)+len(live))
	for _, m := range curated {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	// Split live entries into free vs paid for ordering.
	var liveFree, livePaid []ModelSuggestion
	for _, m := range live {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.Free {
			liveFree = append(liveFree, m)
		} else {
			livePaid = append(livePaid, m)
		}
	}
	sort.Slice(liveFree, func(i, j int) bool { return liveFree[i].Label < liveFree[j].Label })
	sort.Slice(livePaid, func(i, j int) bool { return livePaid[i].Label < livePaid[j].Label })
	out = append(out, liveFree...)
	out = append(out, livePaid...)
	return out
}
