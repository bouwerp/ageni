package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func init() { Register(&openRouterFetcher{}) }

type openRouterFetcher struct{}

func (f *openRouterFetcher) ProviderName() string { return "openrouter" }

func (f *openRouterFetcher) Fetch(ctx context.Context, apiKey string) (*Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://openrouter.ai/api/v1/auth/key", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bodySnippet(resp.Body, 256))
	}

	var payload struct {
		Data struct {
			Label       string  `json:"label"`
			Limit       *int    `json:"limit"`
			LimitRemain *int    `json:"limit_remaining"`
			Usage       float64 `json:"usage"`
			IsFree      bool    `json:"is_free_tier"`
			RateLimit   struct {
				Requests int    `json:"requests"`
				Interval string `json:"interval"`
			} `json:"rate_limit"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	d := payload.Data
	r := &Report{
		ProviderName: "openrouter",
		Label:        "OpenRouter",
		FetchedAt:    time.Now(),
	}
	if d.Limit != nil {
		credits := float64(*d.Limit) - d.Usage
		r.Credits = ptr(credits)
	}
	if d.RateLimit.Requests > 0 {
		r.RateLimitRequests = ptr(d.RateLimit.Requests)
		r.RateLimitInterval = d.RateLimit.Interval
	}
	return r, nil
}
