package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileErrorSuggestsApplyDiffOnMissingMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("package x\n\nfunc Hello() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker := NewChangeTracker(filepath.Join(dir, "changes.jsonl"), filepath.Join(dir, "snapshots"))
	tool := EditFile{Tracker: tracker}
	args, _ := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "func Missing() string { return \"nope\" }",
		"new_string": "func Missing() string { return \"yep\" }",
	})
	_, err := tool.Call(context.Background(), args)
	if err == nil {
		t.Fatal("expected missing match error")
	}
	if !strings.Contains(err.Error(), "prefer apply_diff") {
		t.Fatalf("expected apply_diff guidance, got %q", err.Error())
	}
}

func TestMultiEditErrorSuggestsApplyDiffOnAmbiguousMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("package x\n\nconst Name = \"a\"\nconst Alias = \"a\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker := NewChangeTracker(filepath.Join(dir, "changes.jsonl"), filepath.Join(dir, "snapshots"))
	tool := MultiEdit{Tracker: tracker}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "\"a\"", "new_string": "\"b\""},
		},
	})
	_, err := tool.Call(context.Background(), args)
	if err == nil {
		t.Fatal("expected ambiguous match error")
	}
	if !strings.Contains(err.Error(), "prefer apply_diff") {
		t.Fatalf("expected apply_diff guidance, got %q", err.Error())
	}
}
