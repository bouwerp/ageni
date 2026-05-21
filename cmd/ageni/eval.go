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
	Name            string   `json:"name"`
	Prompt          string   `json:"prompt"`
	Tags            []string `json:"tags,omitempty"`
	WantContains    []string `json:"want_contains,omitempty"`
	WantNotContains []string `json:"want_not_contains,omitempty"`
}

type evalResult struct {
	Name               string   `json:"name"`
	Prompt             string   `json:"prompt"`
	Tags               []string `json:"tags,omitempty"`
	OK                 bool     `json:"ok"`
	DurationMS         int64    `json:"duration_ms"`
	MissingContains    []string `json:"missing_contains,omitempty"`
	UnexpectedContains []string `json:"unexpected_contains,omitempty"`
	Error              string   `json:"error,omitempty"`
	Output             string   `json:"output,omitempty"`
}

type evalReport struct {
	AgeniVersion string       `json:"ageni_version"`
	FixturePath  string       `json:"fixture_path"`
	SelectedTags []string     `json:"selected_tags,omitempty"`
	StartedAt    time.Time    `json:"started_at"`
	FinishedAt   time.Time    `json:"finished_at"`
	Total        int          `json:"total"`
	Passed       int          `json:"passed"`
	Failed       int          `json:"failed"`
	Results      []evalResult `json:"results"`
}

type evalOptions struct {
	Path string
	Out  string
	Tags []string
}

func runEval(args []string) error {
	opts, err := parseEvalArgs(args)
	if err != nil {
		return err
	}
	fixtures, err := loadEvalFixtures(opts.Path)
	if err != nil {
		return err
	}
	fixtures = filterEvalFixtures(fixtures, opts.Tags)
	if len(fixtures) == 0 {
		return fmt.Errorf("no eval fixtures matched %q with tags %v", opts.Path, opts.Tags)
	}
	report := evalReport{
		AgeniVersion: version,
		FixturePath:  opts.Path,
		SelectedTags: append([]string(nil), opts.Tags...),
		StartedAt:    time.Now().UTC(),
		Results:      make([]evalResult, 0, len(fixtures)),
	}
	failed := false
	for _, fixture := range fixtures {
		start := time.Now()
		output, runErr := runHeadlessPrompt(fixture.Prompt)
		result := evalResult{
			Name:       fixture.Name,
			Prompt:     fixture.Prompt,
			Tags:       append([]string(nil), fixture.Tags...),
			DurationMS: time.Since(start).Milliseconds(),
			Output:     output,
		}
		if runErr != nil {
			result.Error = runErr.Error()
		}
		result.MissingContains = missingContains(output, fixture.WantContains)
		result.UnexpectedContains = unexpectedContains(output, fixture.WantNotContains)
		result.OK = result.Error == "" && len(result.MissingContains) == 0 && len(result.UnexpectedContains) == 0
		if !result.OK {
			failed = true
		} else {
			report.Passed++
		}
		report.Results = append(report.Results, result)
	}
	report.Total = len(report.Results)
	report.Failed = report.Total - report.Passed
	report.FinishedAt = time.Now().UTC()
	if err := writeEvalReport(report, opts.Out); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("one or more eval fixtures failed")
	}
	return nil
}

func parseEvalArgs(args []string) (evalOptions, error) {
	var opts evalOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out":
			if i+1 >= len(args) {
				return evalOptions{}, fmt.Errorf("usage: ageni eval [--out file.json] [--tag name] <fixture.json|dir>")
			}
			opts.Out = args[i+1]
			i++
		case "--tag":
			if i+1 >= len(args) {
				return evalOptions{}, fmt.Errorf("usage: ageni eval [--out file.json] [--tag name] <fixture.json|dir>")
			}
			opts.Tags = append(opts.Tags, args[i+1])
			i++
		default:
			if opts.Path != "" {
				return evalOptions{}, fmt.Errorf("usage: ageni eval [--out file.json] [--tag name] <fixture.json|dir>")
			}
			opts.Path = args[i]
		}
	}
	if opts.Path == "" {
		return evalOptions{}, fmt.Errorf("usage: ageni eval [--out file.json] [--tag name] <fixture.json|dir>")
	}
	return opts, nil
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

func filterEvalFixtures(fixtures []evalFixture, tags []string) []evalFixture {
	if len(tags) == 0 {
		return fixtures
	}
	want := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		want[tag] = struct{}{}
	}
	if len(want) == 0 {
		return fixtures
	}
	out := make([]evalFixture, 0, len(fixtures))
	for _, fixture := range fixtures {
		for _, tag := range fixture.Tags {
			if _, ok := want[tag]; ok {
				out = append(out, fixture)
				break
			}
		}
	}
	return out
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

func unexpectedContains(output string, blocked []string) []string {
	unexpected := make([]string, 0, len(blocked))
	for _, needle := range blocked {
		if needle == "" {
			continue
		}
		if strings.Contains(output, needle) {
			unexpected = append(unexpected, needle)
		}
	}
	return unexpected
}

func writeEvalReport(report evalReport, outPath string) error {
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if outPath != "" {
		if dir := filepath.Dir(outPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		if err := os.WriteFile(outPath, body, 0o644); err != nil {
			return err
		}
	}
	_, err = os.Stdout.Write(body)
	return err
}
