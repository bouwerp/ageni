package usage

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Fetcher is implemented by each provider-specific file.
type Fetcher interface {
	ProviderName() string
	Fetch(ctx context.Context, apiKey string) (*Report, error)
}

// Report holds the usage/rate-limit data returned by one provider.
type Report struct {
	ProviderName string
	Label        string
	FetchedAt    time.Time

	// Balance/credits, if the provider exposes them.
	BalanceUSD *float64
	Credits    *float64

	// Rate limit as reported by the API or response headers.
	RateLimitRequests  *int
	RateLimitInterval  string // e.g. "10s", "1m"
	RateLimitRemaining *int
	RateLimitReset     string // time until window resets, e.g. "58s"

	// Error, if the fetch failed.
	Err error
}

var (
	mu       sync.Mutex
	fetchers []Fetcher
)

// Register adds a provider fetcher to the global list. Called from init().
func Register(f Fetcher) {
	mu.Lock()
	defer mu.Unlock()
	fetchers = append(fetchers, f)
}

// FetchAll runs all registered fetchers in parallel, using apiKey(providerName)
// to resolve each key. Returns one Report per registered provider.
func FetchAll(ctx context.Context, apiKey func(string) string) []Report {
	mu.Lock()
	fs := make([]Fetcher, len(fetchers))
	copy(fs, fetchers)
	mu.Unlock()

	results := make([]Report, len(fs))
	var wg sync.WaitGroup
	for i, f := range fs {
		wg.Add(1)
		go func(i int, f Fetcher) {
			defer wg.Done()
			key := apiKey(f.ProviderName())
			if key == "" {
				results[i] = Report{
					ProviderName: f.ProviderName(),
					Err:          fmt.Errorf("no API key configured"),
				}
				return
			}
			r, err := f.Fetch(ctx, key)
			if err != nil {
				results[i] = Report{
					ProviderName: f.ProviderName(),
					Err:          err,
				}
				return
			}
			results[i] = *r
		}(i, f)
	}
	wg.Wait()
	return results
}

func ptr[T any](v T) *T { return &v }
