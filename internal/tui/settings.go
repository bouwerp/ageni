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

	// liveModelCache stores the result of live model fetches keyed by
	// provider name. Populated once by prefetchModels() before the form
	// is built so OptionsFunc closures never block on network I/O.
	liveModelCache map[string][]llm.ModelSuggestion

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

	criticProvider string
	criticModel    string

	maxSubagents   string
	subagentBudget string

	// localFleet is the raw LLAMACPP_FLEET env-var value (user edits it
	// as a text field). localFleetMode is "full", "subset", or "" (disabled).
	localFleet     string
	localFleetMode string

	// subagentPool is the raw SUBAGENT_POOL env-var value — a
	// comma-separated list of "provider" or "provider/model" entries.
	// When set, sub-agents are spread across these providers with
	// registry-guided best-model selection per tier.
	subagentPool string

	// collabMode is the raw AGENI_COLLABORATION_MODE env-var value.
	collabMode string

	// verifyResults is populated by save() with one entry per enabled
	// provider showing the outcome of a quick auth probe. Surfaced to
	// the user as a flash message after save.
	verifyResults []string
}

const leadDisabled = ""

// newSettingsState builds only the settingsState (no huh form). The caller
// owns the provider list component and populates st.enabled / st.keyPtrs from
// it before building the huh form with newSettingsFormFromState.
func newSettingsState() (*settingsState, map[string]string, error) {
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
		criticProvider:  existing["CRITIC_PROVIDER"],
		criticModel:     existing["CRITIC_MODEL"],
		maxSubagents:    orDefault(existing["AGENI_MAX_SUBAGENTS"], "8"),
		subagentBudget:  orDefault(existing["AGENI_SUBAGENT_BUDGET"], "40"),
		localFleet:      existing["LLAMACPP_FLEET"],
		localFleetMode:  existing["LLAMACPP_FLEET_MODE"],
		subagentPool:    existing["SUBAGENT_POOL"],
		collabMode:      orDefault(existing["AGENI_COLLABORATION_MODE"], "off"),
	}
	// Initialise keyPtrs with existing values; the provider list will overwrite
	// them when the user advances to the form phase.
	// Providers that were explicitly disabled in a previous session are saved
	// under AGENI_DISABLED_PROVIDERS so we can restore that state here.
	disabledSet := map[string]bool{}
	for _, name := range strings.Split(existing["AGENI_DISABLED_PROVIDERS"], ",") {
		if name = strings.TrimSpace(name); name != "" {
			disabledSet[name] = true
		}
	}
	for _, p := range llm.AllProviders() {
		key := ""
		if p.APIKeyEnv != "" {
			key = existing[p.APIKeyEnv]
		}
		k := key
		st.keyPtrs[p.Name] = &k
		if (!p.NeedsKey || key != "") && !disabledSet[p.Name] {
			st.enabled = append(st.enabled, p.Name)
		}
	}
	return st, existing, nil
}

// applyProviderList transfers the provider-list component's selections into
// settingsState so save() picks them up.
func (s *settingsState) applyProviderList(pl *providerListModel) {
	s.enabled = pl.enabledNames()
	for name, val := range pl.keyValues() {
		if v, ok := s.keyPtrs[name]; ok {
			*v = val
		} else {
			v := val
			s.keyPtrs[name] = &v
		}
	}
}

// newSettingsFormFromState builds the huh form for role selection / limits.
// The provider section is managed separately by providerListModel.
// Fields are split across focused groups so each "page" covers one topic.
func newSettingsFormFromState(st *settingsState, termHeight int) (*huh.Form, error) {
	// Pre-fetch live model lists once before building the form so that the
	// OptionsFunc closures never block on network I/O during rendering.
	st.prefetchModels()

	groupMaster := huh.NewGroup(
		huh.NewSelect[string]().
			Title("Master · Provider").
			Description("The agent you talk to. Use the best model you can afford.").
			OptionsFunc(func() []huh.Option[string] {
				return enabledProviderOptions(st.enabled)
			}, &st.enabled).
			Value(&st.masterProvider).
			Filtering(true).
			Height(6),
		huh.NewSelect[string]().
			Title("Master · Model").
			OptionsFunc(func() []huh.Option[string] {
				return st.modelOptionsFor(st.masterProvider, st.masterModel)
			}, &st.masterProvider).
			Value(&st.masterModel).
			Filtering(true).
			Height(8),
	)

	groupSub := huh.NewGroup(
		huh.NewSelect[string]().
			Title("Sub-agent · Provider").
			Description("Workers spawned by the master. Cheaper / free-tier is fine.").
			OptionsFunc(func() []huh.Option[string] {
				return enabledProviderOptions(st.enabled)
			}, &st.enabled).
			Value(&st.subProvider).
			Filtering(true).
			Height(6),
		huh.NewSelect[string]().
			Title("Sub-agent · Model").
			OptionsFunc(func() []huh.Option[string] {
				return st.modelOptionsFor(st.subProvider, st.subModel)
			}, &st.subProvider).
			Value(&st.subModel).
			Filtering(true).
			Height(8),
	)

	groupLead := huh.NewGroup(
		huh.NewSelect[string]().
			Title("Lead · Provider  (optional)").
			Description("Used only on the first master turn. (disabled) = master model every turn.").
			OptionsFunc(func() []huh.Option[string] {
				opts := enabledProviderOptions(st.enabled)
				return append([]huh.Option[string]{huh.NewOption("(disabled — every turn uses master model)", leadDisabled)}, opts...)
			}, &st.enabled).
			Value(&st.leadProvider).
			Filtering(true).
			Height(6),
		huh.NewSelect[string]().
			Title("Lead · Model  (optional)").
			OptionsFunc(func() []huh.Option[string] {
				if st.leadProvider == leadDisabled {
					return nil
				}
				return st.modelOptionsFor(st.leadProvider, st.leadModel)
			}, &st.leadProvider).
			Value(&st.leadModel).
			Filtering(true).
			Height(8),
	)

	groupCritic := huh.NewGroup(
		huh.NewSelect[string]().
			Title("Critic · Provider  (optional)").
			Description("Soundboard reviewer. Use a different provider from the master for genuine second opinions.").
			OptionsFunc(func() []huh.Option[string] {
				opts := enabledProviderOptions(st.enabled)
				return append([]huh.Option[string]{huh.NewOption("(disabled — soundboard inactive)", leadDisabled)}, opts...)
			}, &st.enabled).
			Value(&st.criticProvider).
			Filtering(true).
			Height(6),
		huh.NewSelect[string]().
			Title("Critic · Model  (optional)").
			OptionsFunc(func() []huh.Option[string] {
				if st.criticProvider == leadDisabled {
					return nil
				}
				return st.modelOptionsFor(st.criticProvider, st.criticModel)
			}, &st.criticProvider).
			Value(&st.criticModel).
			Filtering(true).
			Height(8),
	)

	groupFallbacks := huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Fallbacks · Master").
			Description("On 429 / 5xx / timeout, ageni tries these providers in order.").
			Options(fallbackOptions(st.enabled, st.masterProvider)...).
			Value(&st.masterFallbacks).
			Filterable(true).
			Height(8),
		huh.NewMultiSelect[string]().
			Title("Fallbacks · Sub-agent").
			Description("Same fallback behaviour for sub-agent workers.").
			Options(fallbackOptions(st.enabled, st.subProvider)...).
			Value(&st.subFallbacks).
			Filterable(true).
			Height(8),
	)

	groupLimits := huh.NewGroup(
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
		huh.NewSelect[string]().
			Title("Collaboration · Mode").
			Description("Multi-LLM teamwork mode (utilizes either the local fleet or cloud providers).").
			Options(
				huh.NewOption("Off — standard single-agent orchestration loop", "off"),
				huh.NewOption("Cascade — escalate tasks sequentially from tiny/fast to capable local/flagship models", "cascade"),
				huh.NewOption("Debate — Developer/Critic peer review loop (stops small local model write-loops)", "debate"),
				huh.NewOption("Self-MoA — parallel trace aggregation (ensembles local fleet/flagship responses)", "self_moa"),
			).
			Value(&st.collabMode),
	)

	groupFleet := huh.NewGroup(
		huh.NewSelect[string]().
			Title("Local Fleet · Mode").
			Description("How locally-hosted llama.cpp workers integrate with cloud sub-agents.").
			Options(
				huh.NewOption("(disabled — use cloud sub-agents only)", ""),
				huh.NewOption("Full fleet — all sub-agents run on local endpoints", "full"),
				huh.NewOption("Subset — haiku-tier tasks → local, sonnet/opus → cloud", "subset"),
			).
			Value(&st.localFleetMode),
		huh.NewInput().
			Title("Local Fleet · Endpoints").
			Description("Comma-separated list of baseURL|model pairs.\nExample: http://localhost:8080/v1|qwen2.5-coder,http://localhost:8081/v1|codestral\nLeave blank to disable.").
			Value(&st.localFleet),
		huh.NewInput().
			Title("Sub-agent Pool · Providers  (optional)").
			Description("Comma-separated cloud providers for sub-agent load balancing.\nThe registry picks the best ROI model per tier from this set.\nExample: anthropic,openai,groq  or  anthropic/claude-haiku-4-5,groq/llama-3.3-70b\nLeave blank to use the single Sub-agent Provider above.").
			Value(&st.subagentPool),
	)

	form := huh.NewForm(groupMaster, groupSub, groupLead, groupCritic, groupFallbacks, groupLimits, groupFleet).
		WithShowHelp(true).
		WithShowErrors(true)
	if termHeight > 0 {
		form.WithHeight(termHeight)
	}
	return form, nil
}

// save writes the edited state back to ~/.ageni/.env, preserving
// unrelated keys, and verifies each enabled provider's API key against
// the provider's /v1/models endpoint. Verification results are stashed
// on st.verifyResults for the caller to surface in the flash message.
func (s *settingsState) save() error {
	existing := config.LoadEnvFile(s.envPath)

	out := config.MergeEnv(existing, map[string]string{
		"MASTER_PROVIDER":          s.masterProvider,
		"SUBAGENT_PROVIDER":        s.subProvider,
		"AGENI_MAX_SUBAGENTS":      s.maxSubagents,
		"AGENI_SUBAGENT_BUDGET":    s.subagentBudget,
		"MASTER_FALLBACKS":         strings.Join(s.masterFallbacks, ","),
		"SUBAGENT_FALLBACKS":       strings.Join(s.subFallbacks, ","),
		"LLAMACPP_FLEET":           strings.TrimSpace(s.localFleet),
		"LLAMACPP_FLEET_MODE":      s.localFleetMode,
		"SUBAGENT_POOL":            strings.TrimSpace(s.subagentPool),
		"AGENI_COLLABORATION_MODE": s.collabMode,
	})

	// Persist which no-key providers the user explicitly disabled so the
	// provider list opens with the correct state next session.
	enabledSet := map[string]bool{}
	for _, name := range s.enabled {
		enabledSet[name] = true
	}
	var disabled []string
	for _, p := range llm.AllProviders() {
		if !p.NeedsKey && !enabledSet[p.Name] {
			disabled = append(disabled, p.Name)
		}
	}
	out["AGENI_DISABLED_PROVIDERS"] = strings.Join(disabled, ",")

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

	if s.criticProvider == leadDisabled {
		out["CRITIC_PROVIDER"] = ""
		out["CRITIC_MODEL"] = ""
	} else {
		criticSpec, _ := llm.LookupProvider(s.criticProvider)
		out["CRITIC_PROVIDER"] = s.criticProvider
		out["CRITIC_MODEL"] = orDefault(s.criticModel, criticSpec.DefaultModel)
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
// description. Kept for potential future use.
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

// prefetchModels populates liveModelCache by fetching live model lists for all
// enabled providers that support it. This is called once before building the
// huh form so that OptionsFunc closures can read from cache without blocking.
func (s *settingsState) prefetchModels() {
	if s.liveModelCache == nil {
		s.liveModelCache = make(map[string][]llm.ModelSuggestion)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	type result struct {
		name   string
		models []llm.ModelSuggestion
	}
	ch := make(chan result, len(s.enabled))
	fetching := 0
	for _, name := range s.enabled {
		if _, cached := s.liveModelCache[name]; cached {
			continue
		}
		spec, ok := llm.LookupProvider(name)
		if !ok || !shouldFetchLive(spec.Name) {
			continue
		}
		fetching++
		go func(spec llm.ProviderSpec) {
			apiKey := os.Getenv(spec.APIKeyEnv)
			live, err := llm.FetchModels(ctx, spec, apiKey)
			if err != nil || len(live) == 0 {
				ch <- result{name: spec.Name}
				return
			}
			ch <- result{name: spec.Name, models: live}
		}(spec)
	}
	for i := 0; i < fetching; i++ {
		r := <-ch
		if len(r.models) > 0 {
			s.liveModelCache[r.name] = r.models
		}
	}
}

// modelOptionsFor returns Select options for a provider's models using the
// pre-fetched liveModelCache. Never blocks on network I/O.
func (s *settingsState) modelOptionsFor(providerName, currentValue string) []huh.Option[string] {
	spec, ok := llm.LookupProvider(providerName)
	if !ok {
		if currentValue != "" {
			return []huh.Option[string]{huh.NewOption(decorateModel("(current) "+currentValue, currentValue, llm.FreeBySpec(currentValue)), currentValue)}
		}
		return nil
	}

	models := append([]llm.ModelSuggestion(nil), spec.RecommendedModels...)
	if live, cached := s.liveModelCache[spec.Name]; cached && len(live) > 0 {
		models = llm.MergeModels(models, live)
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
		opts = append(opts, huh.NewOption(decorateModel("(current) "+currentValue, currentValue, llm.FreeBySpec(currentValue)), currentValue))
		seen[currentValue] = true
	}
	for _, m := range models {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		opts = append(opts, huh.NewOption(decorateModel(m.Label, m.ID, m.Free), m.ID))
	}
	return opts
}

// decorateModel suffixes a model option label with its blended
// ranking score and per-1M price so users can pick a tier-appropriate
// model at a glance. isFree should be set from ModelSuggestion.Free for
// curated models, or from llm.FreeBySpec for current-value lookups.
// Returns the original label unchanged when no ranking / pricing data
// exists, so the picker stays clean for less well-known models.
func decorateModel(label, id string, isFree bool) string {
	parts := []string{label}
	if !strings.Contains(label, id) {
		parts[0] = label + " — " + id
	}
	if r, ok := llm.RankingFor(id); ok {
		parts = append(parts, fmt.Sprintf("rank %d/100", r.Score))
	}
	pr := llm.PricingFor(id)
	switch {
	case isFree || strings.HasSuffix(strings.ToLower(id), ":free"):
		parts = append(parts, "free")
	case pr.Known && pr.InputPer1M == 0 && pr.OutputPer1M == 0:
		// Pricing is explicitly known-zero from the live API (e.g. OpenRouter
		// returning "0"/"0") — display as free.
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

// stringSlicesEqual reports whether two string slices have the same elements
// in the same order.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
