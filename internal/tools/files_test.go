package tools

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestPathFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fallback_test.txt")
	
	// WriteFile fallback test
	wTool := WriteFile{Tracker: NewChangeTracker(filepath.Join(dir, "changes.jsonl"), filepath.Join(dir, "snapshots"))}
	wArgs, _ := json.Marshal(map[string]any{
		"target_file": path,
		"content":     "fallback content works!",
	})
	if _, err := wTool.Call(context.Background(), wArgs); err != nil {
		t.Fatalf("WriteFile failed with fallback: %v", err)
	}

	// ReadFile fallback test
	rTool := ReadFile{}
	rArgs, _ := json.Marshal(map[string]any{
		"absolute_path": path,
	})
	content, err := rTool.Call(context.Background(), rArgs)
	if err != nil {
		t.Fatalf("ReadFile failed with fallback: %v", err)
	}
	if !strings.Contains(content, "fallback content works!") {
		t.Fatalf("unexpected content: %s", content)
	}

	// ReadFile fallback test with AbsolutePath
	rArgs2, _ := json.Marshal(map[string]any{
		"AbsolutePath": path,
	})
	content2, err := rTool.Call(context.Background(), rArgs2)
	if err != nil {
		t.Fatalf("ReadFile failed with fallback AbsolutePath: %v", err)
	}
	if !strings.Contains(content2, "fallback content works!") {
		t.Fatalf("unexpected content: %s", content2)
	}
}

func TestListDirPrefix(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(subDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := ListDir{}
	// Test listing subDir — entries should be prefixed with subDir
	args, _ := json.Marshal(map[string]any{"path": subDir})
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}
	expected := filepath.Join(subDir, "file.txt")
	if !strings.Contains(out, expected) {
		t.Errorf("expected output to contain prefixed path %q, got: %q", expected, out)
	}

	// Test listing CWD / "." — entries should NOT be prefixed
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	if err := os.Chdir(subDir); err != nil {
		t.Fatal(err)
	}
	argsDot, _ := json.Marshal(map[string]any{"path": "."})
	outDot, err := tool.Call(context.Background(), argsDot)
	if err != nil {
		t.Fatalf("ListDir failed on dot: %v", err)
	}
	if outDot != "file.txt" {
		t.Errorf("expected clean base name 'file.txt' when listing '.', got: %q", outDot)
	}
}

func TestReadFileStartLineEndLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "range.txt")
	var body strings.Builder
	for i := 1; i <= 20; i++ {
		body.WriteString(fmt.Sprintf("line %d\n", i))
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := ReadFile{}
	args, _ := json.Marshal(map[string]any{
		"AbsolutePath": path,
		"StartLine":    5,
		"EndLine":      8,
	})
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(out, "lines 5-8 of 20") {
		t.Errorf("expected header to contain lines 5-8 of 20, got: %q", out)
	}
	expectedContent := "line 5\nline 6\nline 7\nline 8\n"
	if !strings.Contains(out, expectedContent) {
		t.Errorf("expected content to contain %q, got: %q", expectedContent, out)
	}
}
