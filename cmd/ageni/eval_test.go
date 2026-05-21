package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadEvalFixtureFileDefaultsName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smoke.json")
	if err := os.WriteFile(path, []byte(`{"prompt":"check auth","want_contains":["auth"]}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fixtures, err := loadEvalFixtureFile(path)
	if err != nil {
		t.Fatalf("loadEvalFixtureFile() error = %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("fixture count = %d, want 1", len(fixtures))
	}
	if fixtures[0].Name != "smoke" {
		t.Fatalf("fixture name = %q, want %q", fixtures[0].Name, "smoke")
	}
}

func TestLoadEvalFixturesDirectorySortsAndFlattens(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.json"), []byte(`[{"name":"b1","prompt":"two"},{"name":"b2","prompt":"three"}]`), 0o644); err != nil {
		t.Fatalf("WriteFile b.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"name":"a1","prompt":"one"}`), 0o644); err != nil {
		t.Fatalf("WriteFile a.json: %v", err)
	}

	fixtures, err := loadEvalFixtures(dir)
	if err != nil {
		t.Fatalf("loadEvalFixtures() error = %v", err)
	}
	gotNames := []string{fixtures[0].Name, fixtures[1].Name, fixtures[2].Name}
	wantNames := []string{"a1", "b1", "b2"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("fixture names = %v, want %v", gotNames, wantNames)
	}
}

func TestMissingContainsReportsMissingNeedles(t *testing.T) {
	got := missingContains("apply_diff is preferred", []string{"apply_diff", "run_tests"})
	want := []string{"run_tests"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missingContains() = %v, want %v", got, want)
	}
}

func TestUnexpectedContainsReportsBlockedNeedles(t *testing.T) {
	got := unexpectedContains("apply_diff is preferred", []string{"edit_file", "apply_diff"})
	want := []string{"apply_diff"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpectedContains() = %v, want %v", got, want)
	}
}

func TestParseEvalArgsAcceptsOutFlag(t *testing.T) {
	got, err := parseEvalArgs([]string{"--out", "results/run.json", "eval/fixtures"})
	if err != nil {
		t.Fatalf("parseEvalArgs() error = %v", err)
	}
	want := evalOptions{Path: "eval/fixtures", Out: "results/run.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEvalArgs() = %+v, want %+v", got, want)
	}
}
