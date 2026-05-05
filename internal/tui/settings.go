package tui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/bouwerp/ageni/internal/config"
	"github.com/bouwerp/ageni/internal/llm"
)

// settingsState holds the in-progress edit values bound to the form fields.
// All fields are flat — the form renders them as a single page with Tab
// navigation, not a multi-step wizard.
type settingsState struct {
	envPath string

	masterProvider string
	masterModel    string
	masterKey      string

	subProvider string
	subModel    string
	subKey      string

	// Optional lead/worker split: lead model used on the FIRST master turn
	// after each user message, regular master model on follow-up
	// execution turns. leadProvider == "" disables the split.
	leadProvider string
	leadModel    string
	leadKey      string

	// Fallback chains: comma-separated "<provider>" or "<provider>/<model>"
	// entries. API keys are pulled from the standard provider env var.
	masterFallbacks string
	subFallbacks    string

	maxSubagents   string
	subagentBudget string
}

// leadDisabled is the sentinel value for "(no lead model)". Stored in the
// state as the empty string so save() omits MASTER_LEAD_PROVIDER entirely.
const leadDisabled = ""

// newSettingsForm builds the settings form pre-filled with the current
// ~/.ageni/.env contents. Single huh.Group → every field is visible
// simultaneously and Tab navigates between them. Returns the state by
// reference; huh mutates it in place as the user edits.
func newSettingsForm() (*huh.Form, *settingsState, error) {
	envPath, err := config.GlobalEnvPath()
	if err != nil {
		return nil, nil, err
	}
	existing := config.LoadEnvFile(envPath)

	st := &settingsState{
		envPath:         envPath,
		masterProvider:  orDefault(existing["MASTER_PROVIDER"], "anthropic"),
		masterModel:     existing["MASTER_MODEL"],
		subProvider:     orDefault(existing["SUBAGENT_PROVIDER"], "anthropic"),
		subModel:        existing["SUBAGENT_MODEL"],
		leadProvider:    existing["MASTER_LEAD_PROVIDER"],
		leadModel:       existing["MASTER_LEAD_MODEL"],
		masterFallbacks: existing["MASTER_FALLBACKS"],
		subFallbacks:    existing["SUBAGENT_FALLBACKS"],
		maxSubagents:    orDefault(existing["AGENI_MAX_SUBAGENTS"], "8"),
		subagentBudget:  orDefault(existing["AGENI_SUBAGENT_BUDGET"], "40"),
	}

	provOpts := providerSelectOptions(false)
	leadOpts := providerSelectOptions(true) // includes a "(disabled)" option

	form := huh.NewForm(
		huh.NewGroup(
			// Master role
			huh.NewNote().Title("Master agent").Description("The agent you talk to. Best model you can afford."),
			huh.NewSelect[string]().
				Title("Provider").
				Options(provOpts...).
				Value(&st.masterProvider).
				Filtering(true).
				Height(8),
			huh.NewSelect[string]().
				Title("Model").
				OptionsFunc(func() []huh.Option[string] {
					return modelOptionsFor(st.masterProvider, st.masterModel)
				}, &st.masterProvider).
				Value(&st.masterModel).
				Filtering(true).
				Height(8),
			huh.NewInput().
				Title("API key (blank = keep existing)").
				EchoMode(huh.EchoModePassword).
				Value(&st.masterKey),

			// Sub-agent role
			huh.NewNote().Title("Sub-agents").Description("Workers spawned by the master. Cheaper / free-tier is fine."),
			huh.NewSelect[string]().
				Title("Provider").
				Options(provOpts...).
				Value(&st.subProvider).
				Filtering(true).
				Height(8),
			huh.NewSelect[string]().
				Title("Model").
				OptionsFunc(func() []huh.Option[string] {
					return modelOptionsFor(st.subProvider, st.subModel)
				}, &st.subProvider).
				Value(&st.subModel).
				Filtering(true).
				Height(8),
			huh.NewInput().
				Title("API key (blank = keep existing)").
				EchoMode(huh.EchoModePassword).
				Value(&st.subKey),

			// Optional lead model — first master turn after each user message
			huh.NewNote().
				Title("Lead model (optional)").
				Description("If set, the FIRST master turn after each user message uses the lead model (planning); subsequent execution turns use the regular master model. Saves tokens at equivalent quality. Pick (disabled) to use the master model for every turn."),
			huh.NewSelect[string]().
				Title("Lead provider").
				Options(leadOpts...).
				Value(&st.leadProvider).
				Filtering(true).
				Height(8),
			huh.NewSelect[string]().
				Title("Lead model").
				OptionsFunc(func() []huh.Option[string] {
					if st.leadProvider == leadDisabled {
						return nil
					}
					return modelOptionsFor(st.leadProvider, st.leadModel)
				}, &st.leadProvider).
				Value(&st.leadModel).
				Filtering(true).
				Height(8),
			huh.NewInput().
				Title("Lead API key (blank = keep existing or use provider default)").
				EchoMode(huh.EchoModePassword).
				Value(&st.leadKey),

			// Fallback chains
			huh.NewNote().
				Title("Fallback chains").
				Description("On 429 / 5xx / timeout / network failure, ageni rolls over to the next entry. Comma-separated 'provider' or 'provider/model' (e.g. 'anthropic/claude-haiku-4-5-20251001,groq,openrouter'). API keys are pulled from each provider's standard env var."),
			huh.NewInput().
				Title("Master fallbacks").
				Value(&st.masterFallbacks).
				Validate(validateFallbackSpec),
			huh.NewInput().
				Title("Sub-agent fallbacks").
				Value(&st.subFallbacks).
				Validate(validateFallbackSpec),

			// Limits
			huh.NewNote().
				Title("Limits").
				Description("Concurrency caps parallel workers; budget caps tool calls per worker."),
			huh.NewInput().
				Title("Max concurrent sub-agents").
				Description("Lower this on rate-limited free tiers.").
				Value(&st.maxSubagents).
				Validate(positiveInt),
			huh.NewInput().
				Title("Default tool-call budget per sub-agent").
				Description("Soft cap. The master can override per-spawn.").
				Value(&st.subagentBudget).
				Validate(positiveInt),
		),
	).WithShowHelp(true).WithShowErrors(true)

	return form, st, nil
}

// save writes the edited values back to ~/.ageni/.env, preserving any keys
// the form doesn't manage (e.g. unrelated env vars the user added by hand).
func (s *settingsState) save() error {
	existing := config.LoadEnvFile(s.envPath)

	masterSpec, _ := llm.LookupProvider(s.masterProvider)
	subSpec, _ := llm.LookupProvider(s.subProvider)

	model := s.masterModel
	if model == "" {
		model = masterSpec.DefaultModel
	}
	subModel := s.subModel
	if subModel == "" {
		subModel = subSpec.DefaultModel
	}

	out := config.MergeEnv(existing, map[string]string{
		"MASTER_PROVIDER":       s.masterProvider,
		"MASTER_MODEL":          model,
		"SUBAGENT_PROVIDER":     s.subProvider,
		"SUBAGENT_MODEL":        subModel,
		"AGENI_MAX_SUBAGENTS":   s.maxSubagents,
		"AGENI_SUBAGENT_BUDGET": s.subagentBudget,
		"MASTER_FALLBACKS":      strings.TrimSpace(s.masterFallbacks),
		"SUBAGENT_FALLBACKS":    strings.TrimSpace(s.subFallbacks),
	})

	// Lead model — empty leadProvider means "disabled". MergeEnv treats
	// an empty value as "delete this key".
	if s.leadProvider == leadDisabled {
		out["MASTER_LEAD_PROVIDER"] = ""
		out["MASTER_LEAD_MODEL"] = ""
	} else {
		leadSpec, _ := llm.LookupProvider(s.leadProvider)
		leadModel := s.leadModel
		if leadModel == "" {
			leadModel = leadSpec.DefaultModel
		}
		out["MASTER_LEAD_PROVIDER"] = s.leadProvider
		out["MASTER_LEAD_MODEL"] = leadModel
		if s.leadKey != "" && leadSpec.APIKeyEnv != "" {
			out["MASTER_LEAD_API_KEY"] = s.leadKey
		}
	}

	// Only update key env vars when the user typed a new value (blank means
	// "keep existing").
	if s.masterKey != "" && masterSpec.APIKeyEnv != "" {
		out[masterSpec.APIKeyEnv] = s.masterKey
	}
	if s.subKey != "" && subSpec.APIKeyEnv != "" {
		out[subSpec.APIKeyEnv] = s.subKey
	}

	return config.WriteEnvFile(s.envPath, out)
}

// providerSelectOptions returns the standard provider list for huh
// Select fields. When includeDisabled is true, prepends a "(disabled —
// use master model)" entry whose value is the empty string — used by
// the optional lead-model picker.
func providerSelectOptions(includeDisabled bool) []huh.Option[string] {
	specs := llm.AllProviders()
	opts := make([]huh.Option[string], 0, len(specs)+1)
	if includeDisabled {
		opts = append(opts, huh.NewOption("(disabled — use master model for every turn)", leadDisabled))
	}
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

// modelOptionsFor returns Select options for a provider, with the
// currently-configured value (if any) pinned at the top so users with custom
// models still see them. For OpenAI-compatible providers we also fetch the
// live /v1/models list so the picker covers everything the provider offers,
// not just our curated subset.
func modelOptionsFor(providerName, currentValue string) []huh.Option[string] {
	spec, ok := llm.LookupProvider(providerName)
	if !ok {
		if currentValue != "" {
			return []huh.Option[string]{huh.NewOption("(current) "+currentValue, currentValue)}
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

	opts := make([]huh.Option[string], 0, len(models)+1)
	seen := map[string]bool{}
	if currentValue != "" {
		opts = append(opts, huh.NewOption("(current) "+currentValue, currentValue))
		seen[currentValue] = true
	}
	for _, m := range models {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		label := m.Label
		if !strings.Contains(label, m.ID) {
			label = m.Label + " — " + m.ID
		}
		opts = append(opts, huh.NewOption(label, m.ID))
	}
	return opts
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

// validateFallbackSpec checks that each entry in a comma-separated
// fallback string names a known provider. Empty input is fine. Helps
// the user catch typos before save without any of the network /
// authentication checks that real fallback resolution does.
func validateFallbackSpec(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, raw := range strings.Split(s, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		provName := entry
		if idx := strings.IndexByte(entry, '/'); idx > 0 {
			provName = entry[:idx]
		}
		if _, ok := llm.LookupProvider(provName); !ok {
			return fmt.Errorf("unknown provider %q in fallback chain", provName)
		}
	}
	return nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
