package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileReturnsUnchangedStubOnRepeat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("package x\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := ReadFile{Cache: NewReadFileCache()}
	args, _ := json.Marshal(map[string]any{"path": path})

	first, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	second, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if !strings.Contains(first, "func Hello") {
		t.Fatalf("first read missing file content: %s", first)
	}
	if !strings.Contains(second, "content unchanged") {
		t.Fatalf("second read should return dedup stub, got: %s", second)
	}

	if err := os.WriteFile(path, []byte("package x\n\nfunc Hello() { println(\"hi\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("third read: %v", err)
	}
	if !strings.Contains(third, "println") {
		t.Fatalf("changed file should return fresh content, got: %s", third)
	}
}

func TestReadFileDefaultLimitIsCapped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	var body strings.Builder
	for i := 0; i < 600; i++ {
		body.WriteString("line\n")
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := ReadFile{}
	args, _ := json.Marshal(map[string]any{"path": path})
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("read big file: %v", err)
	}
	if !strings.Contains(out, "lines 1-500 of 600") {
		t.Fatalf("expected 500-line default cap, got: %s", out[:80])
	}
}

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
