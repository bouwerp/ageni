package tui

import (
	"fmt"
	"strconv"

	"github.com/charmbracelet/huh"

	"github.com/bouwerp/ageni/internal/config"
	"github.com/bouwerp/ageni/internal/llm"
)

// settingsState holds the in-progress edit values bound to the form fields.
type settingsState struct {
	envPath string

	masterProvider string
	masterModel    string
	masterKey      string

	subProvider string
	subModel    string
	subKey      string

	maxSubagents string
}

// newSettingsForm builds a huh.Form pre-filled with the current ~/.ageni/.env
// contents. Returns nil + the displayable error if the form can't be built
// (e.g. home dir is unavailable). The returned state is mutated in-place by
// huh as the user edits.
func newSettingsForm() (*huh.Form, *settingsState, error) {
	envPath, err := config.GlobalEnvPath()
	if err != nil {
		return nil, nil, err
	}
	existing := config.LoadEnvFile(envPath)

	st := &settingsState{
		envPath:        envPath,
		masterProvider: orDefault(existing["MASTER_PROVIDER"], "anthropic"),
		masterModel:    existing["MASTER_MODEL"],
		subProvider:    orDefault(existing["SUBAGENT_PROVIDER"], "anthropic"),
		subModel:       existing["SUBAGENT_MODEL"],
		maxSubagents:   orDefault(existing["AGENI_MAX_SUBAGENTS"], "8"),
	}
	// API keys: blank field means "keep existing". We never display the real
	// key so secrets don't leak through the form.

	provOpts := providerSelectOptions()

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Title("Master agent").Description("The agent you talk to. Best model you can afford."),
			huh.NewSelect[string]().
				Title("Provider").
				Description("Type to filter").
				Options(provOpts...).
				Value(&st.masterProvider).
				Filtering(true).
				Height(12),
			huh.NewInput().
				Title("Model").
				Description("Tab to accept a suggestion, or type any provider-specific ID.").
				Value(&st.masterModel).
				SuggestionsFunc(func() []string {
					return modelSuggestionsFor(st.masterProvider)
				}, &st.masterProvider),
			huh.NewInput().
				Title("API key (leave blank to keep existing)").
				EchoMode(huh.EchoModePassword).
				Value(&st.masterKey),
		),
		huh.NewGroup(
			huh.NewNote().Title("Sub-agents").Description("Workers spawned by the master. Cheaper / free-tier is fine."),
			huh.NewSelect[string]().
				Title("Provider").
				Description("Type to filter").
				Options(provOpts...).
				Value(&st.subProvider).
				Filtering(true).
				Height(12),
			huh.NewInput().
				Title("Model").
				Description("Tab to accept a suggestion, or type any provider-specific ID.").
				Value(&st.subModel).
				SuggestionsFunc(func() []string {
					return modelSuggestionsFor(st.subProvider)
				}, &st.subProvider),
			huh.NewInput().
				Title("API key (leave blank to keep existing)").
				EchoMode(huh.EchoModePassword).
				Value(&st.subKey),
		),
		huh.NewGroup(
			huh.NewNote().Title("Concurrency").Description("Cap on parallel sub-agents. Lower this on rate-limited free tiers."),
			huh.NewInput().
				Title("Max concurrent sub-agents").
				Value(&st.maxSubagents).
				Validate(func(s string) error {
					n, err := strconv.Atoi(s)
					if err != nil || n < 1 {
						return fmt.Errorf("must be a positive integer")
					}
					return nil
				}),
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
		"MASTER_PROVIDER":     s.masterProvider,
		"MASTER_MODEL":        model,
		"SUBAGENT_PROVIDER":   s.subProvider,
		"SUBAGENT_MODEL":      subModel,
		"AGENI_MAX_SUBAGENTS": s.maxSubagents,
	})

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

func providerSelectOptions() []huh.Option[string] {
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

// modelSuggestionsFor returns the recommended model IDs for a provider name,
// for use as autocomplete suggestions on the Input field.
func modelSuggestionsFor(providerName string) []string {
	spec, ok := llm.LookupProvider(providerName)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(spec.RecommendedModels))
	for _, m := range spec.RecommendedModels {
		out = append(out, m.ID)
	}
	return out
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
