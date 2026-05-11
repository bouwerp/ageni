package models

import (
	"context"
	"log"
	"time"
)

const refreshInterval = 5 * time.Minute

// Updater runs a background goroutine that refreshes the Global registry
// by fetching the Aider polyglot leaderboard and OpenRouter availability.
// Call Start once per process; it blocks until ctx is cancelled.
type Updater struct {
	registry *Registry
	// Notify is sent to (non-blocking) after each successful full refresh.
	// Callers may use this to trigger a UI redraw.
	Notify chan struct{}
}

// NewUpdater creates an Updater bound to the given registry.
func NewUpdater(reg *Registry) *Updater {
	return &Updater{
		registry: reg,
		Notify:   make(chan struct{}, 1),
	}
}

// Start launches the background refresh loop. It performs an immediate fetch
// on startup, then repeats every refreshInterval. Returns when ctx is done.
func (u *Updater) Start(ctx context.Context) {
	// First fetch: run in the background so startup is not blocked,
	// but give it a short head start before the periodic ticker.
	go func() {
		u.refresh(ctx)
	}()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.refresh(ctx)
		}
	}
}

// refresh runs one full update cycle: fetch Aider scores, fetch OpenRouter
// availability, and apply both to the registry.
func (u *Updater) refresh(ctx context.Context) {
	aiderOK := false
	scores, err := FetchAiderScores(ctx)
	if err != nil {
		log.Printf("[models] aider fetch: %v", err)
	} else {
		u.registry.ApplyAiderScores(scores)
		aiderOK = true
	}

	avail, costs, err := FetchOpenRouterAvailability(ctx)
	if err != nil {
		log.Printf("[models] openrouter availability fetch: %v", err)
	} else {
		u.registry.ApplyAvailability(avail)
		u.registry.ApplyPricing(costs)
	}

	if aiderOK {
		// Non-blocking notify.
		select {
		case u.Notify <- struct{}{}:
		default:
		}
	}
}
