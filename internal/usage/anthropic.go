package usage

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func init() { Register(&anthropicFetcher{}) }

type anthropicFetcher struct{}

func (f *anthropicFetcher) ProviderName() string { return "anthropic" }

// GET https://api.anthropic.com/v1/models — cheap probe to read rate-limit headers.
func (f *anthropicFetcher) Fetch(ctx context.Context, apiKey string) (*Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("invalid API key (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	r := &Report{
		ProviderName: "anthropic",
		Label:        "Anthropic",
		FetchedAt:    time.Now(),
	}
	parseRateLimitHeaders(resp.Header, "anthropic-ratelimit-requests-limit",
		"anthropic-ratelimit-requests-remaining", "anthropic-ratelimit-requests-reset", r)
	return r, nil
}
