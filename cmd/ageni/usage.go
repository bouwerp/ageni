package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/ageni/internal/config"
	"github.com/bouwerp/ageni/internal/usage"
)

func runUsage() error {
	config.LoadEnv()

	apiKey := func(provider string) string {
		switch provider {
		case "openrouter":
			return os.Getenv("OPENROUTER_API_KEY")
		case "deepseek":
			return os.Getenv("DEEPSEEK_API_KEY")
		case "groq":
			return os.Getenv("GROQ_API_KEY")
		case "together":
			return os.Getenv("TOGETHER_API_KEY")
		case "anthropic":
			return os.Getenv("ANTHROPIC_API_KEY")
		}
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reports := usage.FetchAll(ctx, apiKey)
	printUsageReports(reports)
	return nil
}

func printUsageReports(reports []usage.Report) {
	if len(reports) == 0 {
		fmt.Println("No providers configured.")
		return
	}
	for _, r := range reports {
		label := r.Label
		if label == "" {
			label = r.ProviderName
		}
		fmt.Printf("\n%s\n", label)
		if r.Err != nil {
			fmt.Printf("   error:   %v\n", r.Err)
			continue
		}
		if r.BalanceUSD != nil {
			fmt.Printf("   balance: $%.4f\n", *r.BalanceUSD)
		}
		if r.Credits != nil {
			fmt.Printf("   credits: %.4f\n", *r.Credits)
		}
		if r.RateLimitRequests != nil || r.RateLimitRemaining != nil {
			var parts []string
			if r.RateLimitRequests != nil && r.RateLimitInterval != "" {
				parts = append(parts, fmt.Sprintf("limit %d/%s", *r.RateLimitRequests, r.RateLimitInterval))
			} else if r.RateLimitRequests != nil {
				parts = append(parts, fmt.Sprintf("limit %d", *r.RateLimitRequests))
			}
			if r.RateLimitRemaining != nil {
				parts = append(parts, fmt.Sprintf("%d remaining", *r.RateLimitRemaining))
			}
			if r.RateLimitReset != "" {
				parts = append(parts, fmt.Sprintf("resets %s", r.RateLimitReset))
			}
			fmt.Printf("   rate:    %s\n", strings.Join(parts, ", "))
		}
	}
	fmt.Println()
}
