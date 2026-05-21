package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type evalFixture struct {
	Name         string   `json:"name"`
	Prompt       string   `json:"prompt"`
	WantContains []string `json:"want_contains,omitempty"`
}

type evalResult struct {
	Name            string   `json:"name"`
	OK              bool     `json:"ok"`
	DurationMS      int64    `json:"duration_ms"`
	MissingContains []string `json:"missing_contains,omitempty"`
	Error           string   `json:"error,omitempty"`
	Output          string   `json:"output,omitempty"`
}

func runEval(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: ageni eval <fixture.json|dir>")
	}
	fixtures, err := loadEvalFixtures(args[0])
	if err != nil {
		return err
	}
	results := make([]evalResult, 0, len(fixtures))
	failed := false
	for _, fixture := range fixtures {
		start := time.Now()
		output, runErr := runHeadlessPrompt(fixture.Prompt)
		result := evalResult{
			Name:       fixture.Name,
			DurationMS: time.Since(start).Milliseconds(),
			Output:     output,
		}
		if runErr != nil {
			result.Error = runErr.Error()
		}
		result.MissingContains = missingContains(output, fixture.WantContains)
		result.OK = result.Error == "" && len(result.MissingContains) == 0
		if !result.OK {
			failed = true
		}
		results = append(results, result)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("one or more eval fixtures failed")
	}
	return nil
}

func loadEvalFixtures(path string) ([]evalFixture, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return loadEvalFixtureFile(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, filepath.Join(path, entry.Name()))
	}
	sort.Strings(names)
	var fixtures []evalFixture
	for _, name := range names {
		loaded, err := loadEvalFixtureFile(name)
		if err != nil {
			return nil, err
		}
		fixtures = append(fixtures, loaded...)
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("no eval fixtures found in %s", path)
	}
	return fixtures, nil
}

func loadEvalFixtureFile(path string) ([]evalFixture, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var many []evalFixture
	if err := json.Unmarshal(body, &many); err == nil {
		return normalizeEvalFixtures(path, many)
	}
	var one evalFixture
	if err := json.Unmarshal(body, &one); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return normalizeEvalFixtures(path, []evalFixture{one})
}

func normalizeEvalFixtures(path string, fixtures []evalFixture) ([]evalFixture, error) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	out := make([]evalFixture, 0, len(fixtures))
	for i, fixture := range fixtures {
		fixture.Prompt = strings.TrimSpace(fixture.Prompt)
		if fixture.Prompt == "" {
			return nil, fmt.Errorf("%s fixture %d: prompt is required", path, i+1)
		}
		if fixture.Name == "" {
			if len(fixtures) == 1 {
				fixture.Name = base
			} else {
				fixture.Name = fmt.Sprintf("%s#%d", base, i+1)
			}
		}
		out = append(out, fixture)
	}
	return out, nil
}

func missingContains(output string, want []string) []string {
	missing := make([]string, 0, len(want))
	for _, needle := range want {
		if needle == "" {
			continue
		}
		if !strings.Contains(output, needle) {
			missing = append(missing, needle)
		}
	}
	return missing
}
