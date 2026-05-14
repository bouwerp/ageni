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

	// InputCostPer1M is the input (prompt) cost in USD per million tokens.
	// Seeded statically; overwritten by the live OpenRouter pricing fetcher.
	InputCostPer1M float64

	// OutputCostPer1M is the output (completion) cost in USD per million tokens.
	// Seeded statically; overwritten by the live OpenRouter pricing fetcher.
	OutputCostPer1M float64

	// ROIScore is BlendedScore / effective_cost_per_1M_tokens, where
	// effective_cost uses a 20/80 input/output token ratio typical of agentic
	// workloads. Higher is better (more capability per dollar). 0 = no pricing.
	ROIScore float64

	// Capabilities lists model capabilities beyond basic text generation.
	// Known values: "vision" (accepts image inputs), "reasoning" (explicit
	// chain-of-thought / extended thinking mode with thinking tokens).
	// Populated from seed data; "vision" is also updated live from OpenRouter's
	// architecture.input_modalities field.
	Capabilities []string

	// UpdatedAt records when BlendedScore was last recalculated.
	UpdatedAt time.Time
}

// HasCapability reports whether the model has the named capability.
// cap is one of "vision", "reasoning".
func (m *CanonicalModel) HasCapability(cap string) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
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
		// Static availability: any provider listed in ProviderIDs is considered
		// available by default (it is part of the known/seeded data set).
		for provider := range m.ProviderIDs {
			m.AvailableProviders = append(m.AvailableProviders, provider)
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

// BestForTier returns the highest-ROI model (falling back to BlendedScore when
// ROI is unavailable) for the given canonical tier ("flagship", "mid", "fast",
// "tiny") that is served by at least one of the listed providers.
//
// providers is the list of configured provider names (e.g. "anthropic",
// "openrouter", "openai"). The returned providerID is the specific provider to
// use, and modelID is the provider-specific model string.
//
// Returns nil, "", "" when no match is found (e.g. registry not yet populated
// or no configured provider carries any model for the tier).
func (r *Registry) BestForTier(tier string, providers []string) (model *CanonicalModel, providerID, modelID string) {
	if len(providers) == 0 {
		return nil, "", ""
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, p := range providers {
		providerSet[p] = struct{}{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// ranked is sorted by BlendedScore descending; we want the best
	// ROI-ranked model in tier that has an available provider.
	// Build a candidate list first, then sort by ROI (then BlendedScore).
	type candidate struct {
		m          *CanonicalModel
		provider   string
		specificID string
		roi        float64
	}
	var candidates []candidate
	for _, m := range r.ranked {
		if m.Tier != tier {
			continue
		}
		// Find first available provider from the configured set.
		for _, ap := range m.AvailableProviders {
			if _, ok := providerSet[ap]; ok {
				if sid, ok2 := m.ProviderIDs[ap]; ok2 && sid != "" {
					roi := m.ROIScore
					if roi == 0 {
						roi = m.BlendedScore // fallback ordering
					}
					candidates = append(candidates, candidate{m, ap, sid, roi})
					break
				}
			}
		}
	}
	if len(candidates) == 0 {
		return nil, "", ""
	}
	// Pick highest ROI candidate.
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.roi > best.roi {
			best = c
		}
	}
	return best.m, best.provider, best.specificID
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
// Only the providers represented in the available map are updated; providers
// sourced from the seed (static ProviderIDs) are preserved unchanged.
func (r *Registry) ApplyAvailability(available map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Determine which provider namespaces this live fetch covers (e.g. "openrouter").
	liveProviders := make(map[string]bool)
	for key := range available {
		if idx := strings.Index(key, ":"); idx >= 0 {
			liveProviders[key[:idx]] = true
		}
	}

	// Remove only the live-fetched provider entries so the seed-derived ones
	// (from ProviderIDs) survive.
	for _, m := range r.models {
		kept := m.AvailableProviders[:0]
		for _, p := range m.AvailableProviders {
			if !liveProviders[p] {
				kept = append(kept, p)
			}
		}
		m.AvailableProviders = kept
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

// ApplyPricing stores live input/output costs fetched from OpenRouter.
// costs maps "openrouter:modelID" → [inputCostPerM, outputCostPerM].
func (r *Registry) ApplyPricing(costs map[string][2]float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, pair := range costs {
		cid, ok := r.providerIndex[key]
		if !ok {
			// Try without :free suffix.
			idx := strings.Index(key, ":")
			if idx >= 0 {
				base := strings.TrimSuffix(key[idx+1:], ":free")
				cid, ok = r.providerIndex[key[:idx]+":"+base]
			}
		}
		if !ok {
			continue
		}
		m := r.models[cid]
		if pair[0] > 0 {
			m.InputCostPer1M = pair[0]
		}
		if pair[1] > 0 {
			m.OutputCostPer1M = pair[1]
		}
	}
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
		// ROI = score / effective_cost_per_1M_tokens.
		// Effective cost weights input:output at 20:80 (typical agentic ratio).
		effective := 0.2*m.InputCostPer1M + 0.8*m.OutputCostPer1M
		if effective > 0 && m.BlendedScore > 0 {
			m.ROIScore = m.BlendedScore / effective
		} else {
			m.ROIScore = 0
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
