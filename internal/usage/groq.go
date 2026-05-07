package usage

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func init() { Register(&groqFetcher{}) }

type groqFetcher struct{}

func (f *groqFetcher) ProviderName() string { return "groq" }

// GET https://api.groq.com/openai/v1/models — cheap probe to read rate-limit headers.
func (f *groqFetcher) Fetch(ctx context.Context, apiKey string) (*Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.groq.com/openai/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	r := &Report{
		ProviderName: "groq",
		Label:        "Groq",
		FetchedAt:    time.Now(),
	}
	parseRateLimitHeaders(resp.Header, "x-ratelimit-limit-requests",
		"x-ratelimit-remaining-requests", "x-ratelimit-reset-requests", r)
	return r, nil
}
