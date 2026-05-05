package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSearchReplaceBlocks(t *testing.T) {
	in := "<<<<<<< SEARCH\nfoo\nbar\n=======\nFOO\nBAR\n>>>>>>> REPLACE\nignored prose\n<<<<<<< SEARCH\nbaz\n=======\nBAZ\n>>>>>>> REPLACE\n"
	blocks, err := parseSearchReplaceBlocks(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Search != "foo\nbar" || blocks[0].Replace != "FOO\nBAR" {
		t.Fatalf("block 0: %#v", blocks[0])
	}
	if blocks[1].Search != "baz" || blocks[1].Replace != "BAZ" {
		t.Fatalf("block 1: %#v", blocks[1])
	}
}

func TestApplyDiffSearchReplace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.go")
	original := "package x\n\nfunc Hello() string { return \"hi\" }\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := NewChangeTracker(filepath.Join(dir, "changes.jsonl"), filepath.Join(dir, "snap"))
	tool := ApplyDiff{Tracker: tr}
	args, _ := json.Marshal(map[string]any{
		"path":   target,
		"format": "search_replace",
		"content": "<<<<<<< SEARCH\nfunc Hello() string { return \"hi\" }\n=======\nfunc Hello() string { return \"hello\" }\n>>>>>>> REPLACE\n",
	})
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "applied 1 block") {
		t.Fatalf("got: %s", out)
	}
	body, _ := os.ReadFile(target)
	if !strings.Contains(string(body), "\"hello\"") {
		t.Fatalf("body not updated: %s", body)
	}
}

func TestApplyDiffMissDiagnostic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.go")
	original := "package x\n\nfunc Hello() string {\n\treturn \"hi\"\n}\n\nfunc Bye() string {\n\treturn \"bye\"\n}\n"
	os.WriteFile(target, []byte(original), 0o644)
	tr := NewChangeTracker(filepath.Join(dir, "changes.jsonl"), filepath.Join(dir, "snap"))
	tool := ApplyDiff{Tracker: tr}
	// SEARCH that almost matches but indentation is wrong.
	args, _ := json.Marshal(map[string]any{
		"path":    target,
		"format":  "search_replace",
		"content": "<<<<<<< SEARCH\nfunc Hello() string {\n  return \"hi\"\n}\n=======\nfunc Hello() string {\n\treturn \"hello\"\n}\n>>>>>>> REPLACE\n",
	})
	_, err := tool.Call(context.Background(), args)
	if err == nil {
		t.Fatal("expected miss")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Closest candidates") {
		t.Fatalf("missing candidates section: %s", msg)
	}
	if !strings.Contains(msg, "lines 3-5") {
		t.Fatalf("expected lines 3-5 candidate: %s", msg)
	}
}

func TestApplyDiffWhole(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new.txt")
	tr := NewChangeTracker(filepath.Join(dir, "changes.jsonl"), filepath.Join(dir, "snap"))
	tool := ApplyDiff{Tracker: tr}
	args, _ := json.Marshal(map[string]any{
		"path":    target,
		"format":  "whole",
		"content": "fresh\nbody\n",
	})
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote") {
		t.Fatalf("got: %s", out)
	}
	body, _ := os.ReadFile(target)
	if string(body) != "fresh\nbody\n" {
		t.Fatalf("got: %q", body)
	}
}
