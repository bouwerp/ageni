package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func init() { Register(&deepSeekFetcher{}) }

type deepSeekFetcher struct{}

func (f *deepSeekFetcher) ProviderName() string { return "deepseek" }

func (f *deepSeekFetcher) Fetch(ctx context.Context, apiKey string) (*Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.deepseek.com/user/balance", nil)
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
		IsAvailable bool `json:"is_available"`
		BalanceInfo []struct {
			Currency        string `json:"currency"`
			TotalBalance    string `json:"total_balance"`
			GrantedBalance  string `json:"granted_balance"`
			ToppedUpBalance string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	r := &Report{
		ProviderName: "deepseek",
		Label:        "DeepSeek",
		FetchedAt:    time.Now(),
	}
	for _, b := range payload.BalanceInfo {
		if b.Currency == "USD" {
			if v, err := strconv.ParseFloat(b.TotalBalance, 64); err == nil {
				r.BalanceUSD = ptr(v)
			}
			break
		}
	}
	return r, nil
}
