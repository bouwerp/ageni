// Package models maintains a canonical registry of LLM models with stable
// cross-provider identifiers, benchmark scores, and availability information.
// The registry is populated from a static seed and enriched by background
// fetchers that pull live benchmark data (Aider polyglot leaderboard) and
// provider availability (OpenRouter model list).
package models

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// CanonicalModel represents a model with a stable, provider-independent ID
// that maps to each provider's model identifier string.
type CanonicalModel struct {
	// ID is the stable canonical identifier, e.g. "claude-opus-4", "deepseek-v3".
	// Lower-kebab-case, provider-independent.
	ID string

	// Name is a human-readable display name, e.g. "Claude Opus 4".
	Name string

	// Family groups related models: "claude", "gpt", "gemini", "deepseek", etc.
	Family string

	// Tier is one of "flagship", "mid", "fast", "tiny" — maps to agent tier selection.
	Tier string

	// ProviderIDs maps provider name → model ID for that provider.
	// e.g. "anthropic" → "claude-opus-4-7", "openrouter" → "anthropic/claude-opus-4.7"
	ProviderIDs map[string]string

	// Scores holds raw benchmark results keyed by source name.
	// Known sources: "aider_polyglot" (0-100), "seed_blended" (0-100).
	Scores map[string]float64

	// BlendedScore is a 0-100 composite derived from Scores.
	// Recomputed by the registry on every update. 0 = insufficient data.
	BlendedScore float64

	// Notes is a one-line context string shown in the dashboard.
	Notes string

	// AiderAliases is a list of model name strings as they appear in the Aider
	// polyglot YAML, used to match live fetched scores to this canonical entry.
	// Multiple aliases cover variant names (e.g. with/without date suffixes).
	AiderAliases []string

	// AvailableProviders is refreshed by the OpenRouter availability fetcher.
	// It lists configured provider names that currently serve this model.
	AvailableProviders []string

	// UpdatedAt records when BlendedScore was last recalculated.
	UpdatedAt time.Time
}

// Registry is the thread-safe process-wide store of canonical models.
type Registry struct {
	mu sync.RWMutex

	// models is keyed by canonical ID.
	models map[string]*CanonicalModel

	// ranked is a descending-BlendedScore copy rebuilt on every Recompute.
	ranked []*CanonicalModel

	// providerIndex maps "provider:modelID" → canonical ID for reverse lookup.
	providerIndex map[string]string

	updatedAt time.Time
}

// Global is the process-wide registry, seeded at init time.
var Global = newRegistry()

func newRegistry() *Registry {
	r := &Registry{
		models:        make(map[string]*CanonicalModel),
		providerIndex: make(map[string]string),
	}
	r.seedModels(seedData)
	r.recompute()
	return r
}

// seedModels inserts entries without locking — only called from newRegistry.
func (r *Registry) seedModels(entries []CanonicalModel) {
	for i := range entries {
		m := entries[i] // copy
		if m.ProviderIDs == nil {
			m.ProviderIDs = make(map[string]string)
		}
		if m.Scores == nil {
			m.Scores = make(map[string]float64)
		}
		r.models[m.ID] = &m
		for provider, id := range m.ProviderIDs {
			r.providerIndex[provider+":"+id] = m.ID
		}
	}
}

// Lookup returns the canonical model for a given canonical ID, or nil.
func (r *Registry) Lookup(canonicalID string) *CanonicalModel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.models[canonicalID]
}

// LookupByProviderID returns the canonical model given a provider name and
// provider-specific model ID. Returns nil if not found.
func (r *Registry) LookupByProviderID(provider, modelID string) *CanonicalModel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if cid, ok := r.providerIndex[provider+":"+modelID]; ok {
		return r.models[cid]
	}
	// Strip :free suffix (OpenRouter convention).
	if strings.HasSuffix(modelID, ":free") {
		base := strings.TrimSuffix(modelID, ":free")
		if cid, ok := r.providerIndex[provider+":"+base]; ok {
			return r.models[cid]
		}
	}
	return nil
}

// Ranked returns all models sorted by BlendedScore descending (snapshot copy).
func (r *Registry) Ranked() []*CanonicalModel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*CanonicalModel, len(r.ranked))
	copy(out, r.ranked)
	return out
}

// UpdatedAt returns when the registry was last recomputed.
func (r *Registry) UpdatedAt() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.updatedAt
}

// ApplyAiderScores merges Aider polyglot pass_rate_2 scores from a map of
// normalised model name → score (0-100). The name is matched against canonical
// IDs, provider model IDs, and the aiderNames alias lists in the seed.
func (r *Registry) ApplyAiderScores(scores map[string]float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, score := range scores {
		m := r.resolveAiderNameUnsafe(name)
		if m == nil {
			continue
		}
		// Keep the highest score across multiple Aider run variants
		// (e.g. "gpt-5 (high)" and "gpt-5 (medium)" both map to gpt-5).
		if existing, ok := m.Scores["aider_polyglot"]; !ok || score > existing {
			m.Scores["aider_polyglot"] = score
		}
	}
	r.recomputeUnsafe()
}

// ApplyAvailability refreshes which configured providers currently serve each
// model. available is a set of "provider:modelID" keys (value always true).
func (r *Registry) ApplyAvailability(available map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Clear previous live availability.
	for _, m := range r.models {
		m.AvailableProviders = nil
	}
	for key := range available {
		idx := strings.Index(key, ":")
		if idx < 0 {
			continue
		}
		provider, modelID := key[:idx], key[idx+1:]
		cid, ok := r.providerIndex[key]
		if !ok {
			base := strings.TrimSuffix(modelID, ":free")
			cid, ok = r.providerIndex[provider+":"+base]
		}
		if !ok {
			continue
		}
		m := r.models[cid]
		dup := false
		for _, p := range m.AvailableProviders {
			if p == provider {
				dup = true
				break
			}
		}
		if !dup {
			m.AvailableProviders = append(m.AvailableProviders, provider)
		}
	}
	r.recomputeUnsafe()
}

// resolveAiderNameUnsafe matches an Aider model name string against registry
// entries. Must be called with r.mu held for write.
func (r *Registry) resolveAiderNameUnsafe(name string) *CanonicalModel {
	norm := normaliseAiderName(name)
	// 1. Check canonical IDs.
	for id, m := range r.models {
		if id == norm {
			return m
		}
		// 2. Check aiderNames alias list stored in Scores map under "alias:*".
		for _, alias := range m.AiderAliases {
			if normaliseAiderName(alias) == norm {
				return m
			}
		}
	}
	// 3. Check provider model IDs (strip provider prefix from index key).
	for key, cid := range r.providerIndex {
		provID := key[strings.Index(key, ":")+1:]
		if normaliseAiderName(provID) == norm {
			return r.models[cid]
		}
	}
	return nil
}

// normaliseAiderName strips qualifiers added by the Aider benchmark harness
// so that "gpt-5 (high)" and "gpt-5 (medium)" both normalise to "gpt-5".
func normaliseAiderName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Strip parenthetical qualifiers: " (high)", " (32k think)", " (0528)", etc.
	if i := strings.Index(s, " ("); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	// Strip trailing date suffixes: -YYYYMMDD or -YYYY-MM-DD components.
	// We iterate because a name might end in "-YYYY-MM-DD" (three components).
	for {
		stripped := stripTrailingDate(s)
		if stripped == s {
			break
		}
		s = stripped
	}
	return s
}

// stripTrailingDate removes one trailing date component from s:
//   - "-YYYYMMDD" (8-digit suffix after a hyphen)
//   - "-MM-DD"    (two-digit month-day after year already stripped)
//   - "-YYYY"     (4-digit year on its own)
//
// Returns s unchanged if no date suffix is found.
func stripTrailingDate(s string) string {
	// "-YYYYMMDD": last 9 chars are "-DDDDDDDD" where DDDDDDDD is 8 digits.
	if len(s) >= 9 {
		suffix := s[len(s)-8:]
		allDigits := true
		for _, c := range suffix {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits && s[len(s)-9] == '-' {
			return s[:len(s)-9]
		}
	}
	// "-MM-DD": last 6 chars match "-DD-DD" pattern (both groups exactly 2 digits).
	if len(s) >= 6 && s[len(s)-6] == '-' && s[len(s)-3] == '-' {
		m1 := s[len(s)-5 : len(s)-3]
		m2 := s[len(s)-2:]
		allDigit := func(t string) bool {
			for _, c := range t {
				if c < '0' || c > '9' {
					return false
				}
			}
			return true
		}
		if len(m1) == 2 && len(m2) == 2 && allDigit(m1) && allDigit(m2) {
			return s[:len(s)-6]
		}
	}
	// "-YYYY": last 5 chars are "-DDDD" where DDDD is exactly 4 digits.
	if len(s) >= 5 {
		suffix := s[len(s)-4:]
		allDigits := true
		for _, c := range suffix {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits && s[len(s)-5] == '-' {
			return s[:len(s)-5]
		}
	}
	return s
}

// recompute rebuilds BlendedScores and the ranked slice. Must NOT hold r.mu.
func (r *Registry) recompute() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recomputeUnsafe()
}

func (r *Registry) recomputeUnsafe() {
	now := time.Now()
	for _, m := range r.models {
		m.BlendedScore = blend(m.Scores)
		if m.BlendedScore > 0 {
			m.UpdatedAt = now
		}
	}
	ranked := make([]*CanonicalModel, 0, len(r.models))
	for _, m := range r.models {
		ranked = append(ranked, m)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].BlendedScore != ranked[j].BlendedScore {
			return ranked[i].BlendedScore > ranked[j].BlendedScore
		}
		return ranked[i].ID < ranked[j].ID
	})
	r.ranked = ranked
	r.updatedAt = now
}

// blend computes a weighted average of known benchmark sources.
// Weights reflect how relevant each source is for agent/coding workloads:
//
//	aider_polyglot  60% – coding + tool-use benchmark, highly agent-relevant
//	seed_blended    40% – hand-curated composite (falls back when no live data)
func blend(scores map[string]float64) float64 {
	type src struct {
		key    string
		weight float64
	}
	sources := []src{
		{"aider_polyglot", 0.60},
		{"seed_blended", 0.40},
	}
	total, wSum := 0.0, 0.0
	for _, s := range sources {
		if v, ok := scores[s.key]; ok && v > 0 {
			total += v * s.weight
			wSum += s.weight
		}
	}
	if wSum == 0 {
		return 0
	}
	return total / wSum
}
