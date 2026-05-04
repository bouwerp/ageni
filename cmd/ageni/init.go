package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/bouwerp/ageni/internal/config"
	"github.com/bouwerp/ageni/internal/llm"
)

// runInit walks the user through choosing providers + models for the master
// and sub-agent roles, then writes ~/.ageni/.env.
func runInit() error {
	envPath, err := config.GlobalEnvPath()
	if err != nil {
		return err
	}

	existing := loadExistingEnv(envPath)

	fmt.Println("ageni — first-time setup")
	fmt.Println()
	fmt.Println("You'll pick a provider for the master agent (the one you talk to)")
	fmt.Println("and a separate provider for sub-agents (the workers it spawns).")
	fmt.Println("Tip: master = best you can afford; sub-agents = cheaper or free.")
	fmt.Println()

	probed := probeLocalProviders()

	masterRole, err := pickRole("master", existing, "MASTER", probed, nil)
	if err != nil {
		return err
	}
	subRole, err := pickRole("sub-agent", existing, "SUBAGENT", probed, &masterRole.spec)
	if err != nil {
		return err
	}

	maxSubsDefault := subRole.spec.SuggestedMaxSubagents
	if maxSubsDefault <= 0 {
		maxSubsDefault = 4
	}
	maxSubs := strconv.Itoa(maxSubsDefault)
	if v := existing["AGENI_MAX_SUBAGENTS"]; v != "" {
		maxSubs = v
	}
	if err := huh.NewInput().
		Title("Max concurrent sub-agents").
		Description(fmt.Sprintf("Suggested for %s: %d. Lower this if you hit rate limits.", subRole.spec.Label, maxSubsDefault)).
		Value(&maxSubs).
		Validate(func(s string) error {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return fmt.Errorf("must be a positive integer")
			}
			return nil
		}).
		Run(); err != nil {
		return err
	}

	out := mergeEnv(existing, map[string]string{
		"MASTER_PROVIDER":     masterRole.spec.Name,
		"MASTER_MODEL":        masterRole.model,
		"SUBAGENT_PROVIDER":   subRole.spec.Name,
		"SUBAGENT_MODEL":      subRole.model,
		"AGENI_MAX_SUBAGENTS": maxSubs,
	})
	// Optional overrides only if user supplied them.
	setOrClear(out, "MASTER_BASE_URL", masterRole.baseURL, masterRole.spec.BaseURL)
	setOrClear(out, "SUBAGENT_BASE_URL", subRole.baseURL, subRole.spec.BaseURL)
	setOrClear(out, "MASTER_API_KEY", masterRole.roleKey, "")
	setOrClear(out, "SUBAGENT_API_KEY", subRole.roleKey, "")
	if masterRole.spec.APIKeyEnv != "" && masterRole.providerKey != "" {
		out[masterRole.spec.APIKeyEnv] = masterRole.providerKey
	}
	if subRole.spec.APIKeyEnv != "" && subRole.providerKey != "" {
		out[subRole.spec.APIKeyEnv] = subRole.providerKey
	}

	if err := writeEnv(envPath, out); err != nil {
		return fmt.Errorf("write %s: %w", envPath, err)
	}

	fmt.Println()
	fmt.Printf("Wrote %s\n", envPath)
	fmt.Println()
	fmt.Println("Configuration:")
	fmt.Printf("  Master:    %s / %s\n", masterRole.spec.Label, masterRole.model)
	fmt.Printf("  Sub-agent: %s / %s\n", subRole.spec.Label, subRole.model)
	fmt.Printf("  Max concurrent sub-agents: %s\n", maxSubs)
	fmt.Println()
	fmt.Println("Run `ageni` to start.")
	return nil
}

type roleChoice struct {
	spec        llm.ProviderSpec
	model       string
	baseURL     string // override (empty if using preset BaseURL)
	roleKey     string // role-specific key override (rare)
	providerKey string // provider-default key (e.g. ANTHROPIC_API_KEY)
}

// pickRole runs the per-role flow: pick provider, model, key (if needed).
// `master` is the already-chosen master role (nil for the master pick) so we
// can offer a "same as master" shortcut for sub-agents on the same provider.
func pickRole(role string, existing map[string]string, prefix string, probed map[string]bool, master *llm.ProviderSpec) (roleChoice, error) {
	cs := roleChoice{}

	specs := llm.AllProviders()

	// Provider select.
	options := make([]huh.Option[string], 0, len(specs))
	for _, s := range specs {
		label := buildProviderLabel(s, probed)
		options = append(options, huh.NewOption(label, s.Name))
	}
	chosenName := existing[prefix+"_PROVIDER"]
	if chosenName == "" && master != nil {
		chosenName = master.Name
	}
	if chosenName == "" {
		chosenName = "anthropic"
	}
	if err := huh.NewSelect[string]().
		Title(fmt.Sprintf("Pick a provider for the %s agent", role)).
		Description(roleHint(role)).
		Options(options...).
		Value(&chosenName).
		Height(20).
		Run(); err != nil {
		return cs, err
	}
	spec, _ := llm.LookupProvider(chosenName)
	cs.spec = spec

	// Model select.
	model := existing[prefix+"_MODEL"]
	if err := selectModel(role, &spec, &model); err != nil {
		return cs, err
	}
	cs.model = model

	// Custom: ask for base URL if missing.
	if spec.Name == "custom" {
		base := existing[prefix+"_BASE_URL"]
		if err := huh.NewInput().
			Title("Base URL for the custom endpoint").
			Description("Example: http://localhost:4242/v1").
			Value(&base).
			Validate(validateURL).
			Run(); err != nil {
			return cs, err
		}
		cs.baseURL = strings.TrimSpace(base)
	}

	// API key prompt.
	if spec.NeedsKey {
		key := existing[spec.APIKeyEnv]
		if key == "" {
			key = existing[prefix+"_API_KEY"]
		}
		title := fmt.Sprintf("%s API key (set as %s)", spec.Label, spec.APIKeyEnv)
		desc := "Hidden input. Press Enter to keep existing if already set."
		if key != "" {
			desc = fmt.Sprintf("Existing key set (%s). Press Enter to keep, or type a new one.", maskKey(key))
		}
		var entered string
		if err := huh.NewInput().
			Title(title).
			Description(desc).
			EchoMode(huh.EchoModePassword).
			Value(&entered).
			Run(); err != nil {
			return cs, err
		}
		if entered != "" {
			key = strings.TrimSpace(entered)
		}
		if key == "" {
			return cs, fmt.Errorf("API key is required for %s", spec.Label)
		}
		cs.providerKey = key
	} else if spec.Name == "custom" {
		// Custom may or may not need auth.
		var enableKey bool
		if err := huh.NewConfirm().
			Title("Does this endpoint require an API key?").
			Affirmative("Yes").
			Negative("No").
			Value(&enableKey).
			Run(); err != nil {
			return cs, err
		}
		if enableKey {
			var k string
			if err := huh.NewInput().
				Title("API key").
				EchoMode(huh.EchoModePassword).
				Value(&k).
				Run(); err != nil {
				return cs, err
			}
			cs.roleKey = strings.TrimSpace(k)
		}
	}

	return cs, nil
}

func selectModel(role string, spec *llm.ProviderSpec, current *string) error {
	models := append([]llm.ModelSuggestion(nil), spec.RecommendedModels...)
	// For Ollama, fetch installed models live and prepend.
	if spec.Name == "ollama" {
		if live := fetchOllamaModels(); len(live) > 0 {
			seen := map[string]bool{}
			for _, m := range models {
				seen[m.ID] = true
			}
			var prepend []llm.ModelSuggestion
			for _, id := range live {
				if !seen[id] {
					prepend = append(prepend, llm.ModelSuggestion{ID: id, Label: id + " (installed)", Free: true})
				}
			}
			models = append(prepend, models...)
		}
	}

	// Sort: free first, then alpha by label.
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Free != models[j].Free {
			return models[i].Free
		}
		return models[i].Label < models[j].Label
	})

	options := make([]huh.Option[string], 0, len(models)+1)
	for _, m := range models {
		options = append(options, huh.NewOption(m.Label+" — "+m.ID, m.ID))
	}
	options = append(options, huh.NewOption("(custom — type a model name)", "__custom__"))

	picked := *current
	if picked == "" {
		picked = spec.DefaultModel
	}
	for _, m := range models {
		if m.ID == picked {
			goto found
		}
	}
	picked = ""
found:

	if len(options) > 1 {
		if err := huh.NewSelect[string]().
			Title(fmt.Sprintf("Pick a model for %s on %s", role, spec.Label)).
			Description(spec.KnownLimits).
			Options(options...).
			Value(&picked).
			Height(15).
			Run(); err != nil {
			return err
		}
	}

	if picked == "__custom__" || picked == "" {
		var custom string
		if *current != "" {
			custom = *current
		} else {
			custom = spec.DefaultModel
		}
		if err := huh.NewInput().
			Title("Model name").
			Description("Provider-specific model identifier.").
			Value(&custom).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("model name is required")
				}
				return nil
			}).
			Run(); err != nil {
			return err
		}
		picked = strings.TrimSpace(custom)
	}
	*current = picked
	return nil
}

// probeLocalProviders does a 200ms TCP probe of the well-known local-server
// ports to mark them as "available" in the wizard.
func probeLocalProviders() map[string]bool {
	out := map[string]bool{}
	checks := map[string]string{
		"ollama":   "127.0.0.1:11434",
		"llamacpp": "127.0.0.1:8080",
		"vllm":     "127.0.0.1:8000",
	}
	for name, addr := range checks {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			out[name] = true
		}
	}
	return out
}

// fetchOllamaModels returns the names of locally installed Ollama models, or
// nil if Ollama isn't running.
func fetchOllamaModels() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost:11434/api/tags", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	out := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, m.Name)
	}
	return out
}

func buildProviderLabel(s llm.ProviderSpec, probed map[string]bool) string {
	tags := make([]string, 0, 3)
	if s.Free {
		tags = append(tags, "free")
	} else {
		tags = append(tags, "paid")
	}
	if s.Local {
		if probed[s.Name] {
			tags = append(tags, "running")
		} else {
			tags = append(tags, "local")
		}
	}
	if s.SupportsCaching {
		tags = append(tags, "caching")
	}
	tagStr := ""
	if len(tags) > 0 {
		tagStr = " [" + strings.Join(tags, ", ") + "]"
	}
	return fmt.Sprintf("%s%s — %s", s.Label, tagStr, s.Description)
}

func roleHint(role string) string {
	switch role {
	case "master":
		return "The master plans and orchestrates. Use the strongest model you can afford; caching matters here."
	default:
		return "Sub-agents do the actual work. Cheaper / free is fine; tool-call reliability matters."
	}
}

func validateURL(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("URL is required")
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("must be a full URL like http://host:port/v1")
	}
	return nil
}

func maskKey(k string) string {
	if len(k) <= 6 {
		return strings.Repeat("•", len(k))
	}
	return k[:3] + strings.Repeat("•", len(k)-6) + k[len(k)-3:]
}

// loadExistingEnv reads simple KEY=VALUE pairs from a file, ignoring blanks
// and comments. Returns an empty map if the file doesn't exist.
func loadExistingEnv(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			k := strings.TrimSpace(line[:i])
			v := strings.TrimSpace(line[i+1:])
			v = strings.Trim(v, `"'`)
			out[k] = v
		}
	}
	return out
}

// mergeEnv overlays new values on top of existing.
func mergeEnv(base, overlay map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

// setOrClear writes value if it differs from the implicit default; otherwise
// removes the key so the file stays tidy.
func setOrClear(m map[string]string, key, value, defaultValue string) {
	if value == "" || value == defaultValue {
		delete(m, key)
		return
	}
	m[key] = value
}

func writeEnv(path string, kv map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("# Generated by `ageni init`. Edit by hand or rerun the wizard.\n")
	for _, k := range keys {
		v := kv[k]
		if needsQuote(v) {
			v = strconv.Quote(v)
		}
		sb.WriteString(fmt.Sprintf("%s=%s\n", k, v))
	}
	// 0600 — file contains API keys.
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

func needsQuote(v string) bool {
	return strings.ContainsAny(v, " \t\"'#=")
}
