package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/bouwerp/ageni/internal/config"
	"github.com/bouwerp/ageni/internal/llm"
)

// settingsState holds the in-progress edit values bound to the form.
// The form is a single huh.Group so all fields render at once and Tab
// navigates between them. Each field Title carries a "Section · Field"
// prefix so the user always knows where they are regardless of scroll.
//
// keyPtrs[providerName] is a stable *string bound to a huh.NewInput.
// We can't put keys in a map field because huh needs addresses that
// don't move between renders.
type settingsState struct {
	envPath string

	// enabled is the multi-select result — provider names the user has
	// ticked. Drives the option lists for role selection + fallbacks.
	enabled []string

	// keyPtrs holds per-provider API key inputs. Pre-populated from
	// the current env file; blank values are skipped on save.
	keyPtrs map[string]*string

	masterProvider string
	masterModel    string
	subProvider    string
	subModel       string
	leadProvider   string
	leadModel      string

	masterFallbacks []string // provider names; multi-select
	subFallbacks    []string // provider names; multi-select

	maxSubagents   string
	subagentBudget string

	// verifyResults is populated by save() with one entry per enabled
	// provider showing the outcome of a quick auth probe. Surfaced to
	// the user as a flash message after save.
	verifyResults []string
}

const leadDisabled = ""

func newSettingsForm() (*huh.Form, *settingsState, error) {
	envPath, err := config.GlobalEnvPath()
	if err != nil {
		return nil, nil, err
	}
	existing := config.LoadEnvFile(envPath)

	st := &settingsState{
		envPath:         envPath,
		keyPtrs:         make(map[string]*string),
		masterProvider:  orDefault(existing["MASTER_PROVIDER"], "anthropic"),
		masterModel:     existing["MASTER_MODEL"],
		subProvider:     orDefault(existing["SUBAGENT_PROVIDER"], "anthropic"),
		subModel:        existing["SUBAGENT_MODEL"],
		leadProvider:    existing["MASTER_LEAD_PROVIDER"],
		leadModel:       existing["MASTER_LEAD_MODEL"],
		masterFallbacks: parseFallbackProviders(existing["MASTER_FALLBACKS"]),
		subFallbacks:    parseFallbackProviders(existing["SUBAGENT_FALLBACKS"]),
		maxSubagents:    orDefault(existing["AGENI_MAX_SUBAGENTS"], "8"),
		subagentBudget:  orDefault(existing["AGENI_SUBAGENT_BUDGET"], "40"),
	}

	// Pre-populate keyPtrs and the enabled list. A provider counts as
	// enabled if it has a key in the env (or if it's a local provider
	// that doesn't need one).
	for _, p := range llm.AllProviders() {
		key := ""
		if p.APIKeyEnv != "" {
			key = existing[p.APIKeyEnv]
		}
		k := key
		st.keyPtrs[p.Name] = &k
		if !p.NeedsKey || key != "" {
			st.enabled = append(st.enabled, p.Name)
		}
	}

	// Build the form. A single Group renders all fields at once; each
	// field Title carries its section prefix so it's readable regardless
	// of scroll position ("Master · Provider" rather than a separate Note
	// that drifts off-screen when tabbing).
	fields := []huh.Field{
		huh.NewMultiSelect[string]().
			Title("Providers · Enabled").
			Description("Space to tick. Keys are pulled from each provider's standard env var on save and verified against /v1/models.").
			Options(allProviderOptions()...).
			Value(&st.enabled).
			Filterable(true).
			Height(10),
	}

	// Per-provider key inputs — one per provider that needs a key.
	for _, p := range llm.AllProviders() {
		if !p.NeedsKey {
			continue
		}
		title := "Providers · " + p.Label + " key"
		if v := *st.keyPtrs[p.Name]; v != "" {
			title += "  [set]"
		}
		fields = append(fields, huh.NewInput().
			Title(title).
			Description("blank = keep existing").
			EchoMode(huh.EchoModePassword).
			Value(st.keyPtrs[p.Name]),
		)
	}

	// Role selection — only shows enabled providers.
	fields = append(fields,
		huh.NewSelect[string]().
			Title("Master · Provider").
			Description("The agent you talk to. Use the best model you can afford.").
			OptionsFunc(func() []huh.Option[string] {
				return enabledProviderOptions(st.enabled)
			}, &st.enabled).
			Value(&st.masterProvider).
			Filtering(true).
			Height(8),
		huh.NewSelect[string]().
			Title("Master · Model").
			OptionsFunc(func() []huh.Option[string] {
				return modelOptionsFor(st.masterProvider, st.masterModel)
			}, &st.masterProvider).
			Value(&st.masterModel).
			Filtering(true).
			Height(10),

		huh.NewSelect[string]().
			Title("Sub-agent · Provider").
			Description("Workers spawned by the master. Cheaper / free-tier is fine.").
			OptionsFunc(func() []huh.Option[string] {
				return enabledProviderOptions(st.enabled)
			}, &st.enabled).
			Value(&st.subProvider).
			Filtering(true).
			Height(8),
		huh.NewSelect[string]().
			Title("Sub-agent · Model").
			OptionsFunc(func() []huh.Option[string] {
				return modelOptionsFor(st.subProvider, st.subModel)
			}, &st.subProvider).
			Value(&st.subModel).
			Filtering(true).
			Height(10),

		huh.NewSelect[string]().
			Title("Lead · Provider  (optional)").
			Description("Used only on the first master turn (planning). (disabled) = master model every turn.").
			OptionsFunc(func() []huh.Option[string] {
				opts := enabledProviderOptions(st.enabled)
				return append([]huh.Option[string]{huh.NewOption("(disabled — every turn uses master model)", leadDisabled)}, opts...)
			}, &st.enabled).
			Value(&st.leadProvider).
			Filtering(true).
			Height(8),
		huh.NewSelect[string]().
			Title("Lead · Model  (optional)").
			OptionsFunc(func() []huh.Option[string] {
				if st.leadProvider == leadDisabled {
					return nil
				}
				return modelOptionsFor(st.leadProvider, st.leadModel)
			}, &st.leadProvider).
			Value(&st.leadModel).
			Filtering(true).
			Height(10),

		huh.NewMultiSelect[string]().
			Title("Fallbacks · Master").
			Description("On 429 / 5xx / timeout, ageni tries these providers in order. Each uses its default model.").
			OptionsFunc(func() []huh.Option[string] {
				return fallbackOptions(st.enabled, st.masterProvider)
			}, &st.enabled).
			Value(&st.masterFallbacks).
			Filterable(true).
			Height(8),
		huh.NewMultiSelect[string]().
			Title("Fallbacks · Sub-agent").
			Description("Same fallback behaviour for sub-agent workers.").
			OptionsFunc(func() []huh.Option[string] {
				return fallbackOptions(st.enabled, st.subProvider)
			}, &st.enabled).
			Value(&st.subFallbacks).
			Filterable(true).
			Height(8),

		huh.NewInput().
			Title("Limits · Max concurrent sub-agents").
			Description("Lower on rate-limited free tiers.").
			Value(&st.maxSubagents).
			Validate(positiveInt),
		huh.NewInput().
			Title("Limits · Budget per sub-agent").
			Description("Soft tool-call cap. The master can override per-spawn.").
			Value(&st.subagentBudget).
			Validate(positiveInt),
	)

	form := huh.NewForm(huh.NewGroup(fields...)).
		WithShowHelp(true).
		WithShowErrors(true)
	return form, st, nil
}

// save writes the edited state back to ~/.ageni/.env, preserving
// unrelated keys, and verifies each enabled provider's API key against
// the provider's /v1/models endpoint. Verification results are stashed
// on st.verifyResults for the caller to surface in the flash message.
func (s *settingsState) save() error {
	existing := config.LoadEnvFile(s.envPath)

	out := config.MergeEnv(existing, map[string]string{
		"MASTER_PROVIDER":       s.masterProvider,
		"SUBAGENT_PROVIDER":     s.subProvider,
		"AGENI_MAX_SUBAGENTS":   s.maxSubagents,
		"AGENI_SUBAGENT_BUDGET": s.subagentBudget,
		"MASTER_FALLBACKS":      strings.Join(s.masterFallbacks, ","),
		"SUBAGENT_FALLBACKS":    strings.Join(s.subFallbacks, ","),
	})

	// Models default to provider defaults when unset.
	masterSpec, _ := llm.LookupProvider(s.masterProvider)
	subSpec, _ := llm.LookupProvider(s.subProvider)
	out["MASTER_MODEL"] = orDefault(s.masterModel, masterSpec.DefaultModel)
	out["SUBAGENT_MODEL"] = orDefault(s.subModel, subSpec.DefaultModel)

	if s.leadProvider == leadDisabled {
		out["MASTER_LEAD_PROVIDER"] = ""
		out["MASTER_LEAD_MODEL"] = ""
	} else {
		leadSpec, _ := llm.LookupProvider(s.leadProvider)
		out["MASTER_LEAD_PROVIDER"] = s.leadProvider
		out["MASTER_LEAD_MODEL"] = orDefault(s.leadModel, leadSpec.DefaultModel)
	}

	// API keys: write whatever the user typed, blank-skip otherwise.
	for _, p := range llm.AllProviders() {
		if !p.NeedsKey || p.APIKeyEnv == "" {
			continue
		}
		key := *s.keyPtrs[p.Name]
		if key == "" {
			continue
		}
		out[p.APIKeyEnv] = key
	}

	if err := config.WriteEnvFile(s.envPath, out); err != nil {
		return err
	}

	// Verify each enabled provider with a quick auth probe. Results are
	// best-effort; verification errors don't fail the save (the user
	// might be on a flaky network and we don't want to lose their edits).
	s.verifyResults = nil
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, name := range s.enabled {
		spec, ok := llm.LookupProvider(name)
		if !ok {
			continue
		}
		key := ""
		if ptr := s.keyPtrs[name]; ptr != nil {
			key = *ptr
		}
		if spec.NeedsKey && key == "" {
			// Maybe the user has it in a separate env var; fall back to that.
			if spec.APIKeyEnv != "" {
				key = out[spec.APIKeyEnv]
			}
		}
		if err := llm.VerifyKey(ctx, spec, key); err != nil {
			s.verifyResults = append(s.verifyResults, fmt.Sprintf("%s: %s", spec.Label, err.Error()))
		} else {
			s.verifyResults = append(s.verifyResults, fmt.Sprintf("%s: ok", spec.Label))
		}
	}
	return nil
}

// allProviderOptions builds the multi-select options for "enabled
// providers". Each label includes the free/paid/local tag and a short
// description.
func allProviderOptions() []huh.Option[string] {
	specs := llm.AllProviders()
	opts := make([]huh.Option[string], 0, len(specs))
	for _, p := range specs {
		tag := "paid"
		if p.Free {
			tag = "free"
		}
		if p.Local {
			tag = "local"
		}
		label := fmt.Sprintf("%s [%s] — %s", p.Label, tag, p.Description)
		opts = append(opts, huh.NewOption(label, p.Name))
	}
	return opts
}

// enabledProviderOptions filters the provider catalogue down to the
// names in `enabled`. Used for role-selection dropdowns so the user
// can't pick a provider they haven't enabled.
func enabledProviderOptions(enabled []string) []huh.Option[string] {
	if len(enabled) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, n := range enabled {
		set[n] = true
	}
	specs := llm.AllProviders()
	opts := make([]huh.Option[string], 0, len(specs))
	for _, p := range specs {
		if !set[p.Name] {
			continue
		}
		tag := "paid"
		if p.Free {
			tag = "free"
		}
		if p.Local {
			tag = "local"
		}
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s [%s]", p.Label, tag), p.Name))
	}
	return opts
}

// fallbackOptions returns enabled providers excluding `primary` (you
// can't fall back to yourself).
func fallbackOptions(enabled []string, primary string) []huh.Option[string] {
	all := enabledProviderOptions(enabled)
	out := make([]huh.Option[string], 0, len(all))
	for _, o := range all {
		if o.Value == primary {
			continue
		}
		out = append(out, o)
	}
	return out
}

// modelOptionsFor returns Select options for a provider's models, with
// the currently-configured value pinned at the top so users with
// custom IDs still see them. Each label is enriched with a 0-100
// blended ranking score (when known) and price-per-1M-tokens (when
// known), so the picker is informative at a glance.
func modelOptionsFor(providerName, currentValue string) []huh.Option[string] {
	spec, ok := llm.LookupProvider(providerName)
	if !ok {
		if currentValue != "" {
			return []huh.Option[string]{huh.NewOption(decorateModel("(current) "+currentValue, currentValue), currentValue)}
		}
		return nil
	}

	apiKey := os.Getenv(spec.APIKeyEnv)
	models := append([]llm.ModelSuggestion(nil), spec.RecommendedModels...)
	if shouldFetchLive(spec.Name) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if live, err := llm.FetchModels(ctx, spec, apiKey); err == nil && len(live) > 0 {
			models = llm.MergeModels(models, live)
		}
		cancel()
	}

	// Sort by ranking desc (unranked at the bottom), so the strongest
	// model bubbles to the top.
	sort.SliceStable(models, func(i, j int) bool {
		ri, _ := llm.RankingFor(models[i].ID)
		rj, _ := llm.RankingFor(models[j].ID)
		return ri.Score > rj.Score
	})

	opts := make([]huh.Option[string], 0, len(models)+1)
	seen := map[string]bool{}
	if currentValue != "" {
		opts = append(opts, huh.NewOption(decorateModel("(current) "+currentValue, currentValue), currentValue))
		seen[currentValue] = true
	}
	for _, m := range models {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		opts = append(opts, huh.NewOption(decorateModel(m.Label, m.ID), m.ID))
	}
	return opts
}

// decorateModel suffixes a model option label with its blended
// ranking score and per-1M price so users can pick a tier-appropriate
// model at a glance. Returns the original label unchanged when no
// ranking / pricing data exists, so the picker stays clean for less
// well-known models.
func decorateModel(label, id string) string {
	parts := []string{label}
	if !strings.Contains(label, id) {
		parts[0] = label + " — " + id
	}
	if r, ok := llm.RankingFor(id); ok {
		parts = append(parts, fmt.Sprintf("rank %d/100", r.Score))
	}
	pr := llm.PricingFor(id)
	switch {
	case strings.HasSuffix(strings.ToLower(id), ":free"):
		parts = append(parts, "free")
	case pr.Known && pr.InputPer1M == 0 && pr.OutputPer1M == 0:
		parts = append(parts, "free")
	case pr.Known && pr.InputPer1M > 0:
		parts = append(parts, fmt.Sprintf("$%.2f in / $%.2f out per 1M", pr.InputPer1M, pr.OutputPer1M))
	}
	return strings.Join(parts, "  ·  ")
}

// parseFallbackProviders extracts the provider-name prefix of each
// comma-separated entry. Used to round-trip the fallback chain through
// huh.MultiSelect, which works on []string of provider names —
// per-fallback model overrides are deferred to a later release.
func parseFallbackProviders(spec string) []string {
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range strings.Split(spec, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		name := entry
		if idx := strings.IndexByte(entry, '/'); idx > 0 {
			name = entry[:idx]
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func shouldFetchLive(name string) bool {
	switch name {
	case "anthropic", "ollama", "llamacpp", "vllm", "custom":
		return false
	}
	return true
}

func positiveInt(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return fmt.Errorf("must be a positive integer")
	}
	return nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
