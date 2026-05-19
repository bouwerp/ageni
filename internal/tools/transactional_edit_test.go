package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransactionalEditAppliesAllChanges(t *testing.T) {
	dir := t.TempDir()
	meta := filepath.Join(dir, "changes.jsonl")
	snaps := filepath.Join(dir, "snapshots")
	tracker := NewChangeTracker(meta, snaps)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := TransactionalEdit{Tracker: tracker}
	args, _ := json.Marshal(map[string]any{
		"changes": []map[string]any{
			{"path": filepath.Join(dir, "a.txt"), "edits": []map[string]any{{"old_string": "hello", "new_string": "hi"}}},
			{"path": filepath.Join(dir, "b.txt"), "content": "new file\n"},
		},
	})
	if _, err := tool.Call(context.Background(), args); err != nil {
		t.Fatalf("Call(): %v", err)
	}
	gotA, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	gotB, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
	if string(gotA) != "hi\n" || string(gotB) != "new file\n" {
		t.Fatalf("unexpected file contents: a=%q b=%q", gotA, gotB)
	}
}

func TestTransactionalEditRollsBackOnValidationFailure(t *testing.T) {
	dir := t.TempDir()
	meta := filepath.Join(dir, "changes.jsonl")
	snaps := filepath.Join(dir, "snapshots")
	tracker := NewChangeTracker(meta, snaps)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	tool := TransactionalEdit{Tracker: tracker}
	args, _ := json.Marshal(map[string]any{
		"changes": []map[string]any{
			{"path": path, "edits": []map[string]any{{"old_string": "before", "new_string": "after"}}},
		},
		"validate_command": "false",
	})
	if _, err := tool.Call(context.Background(), args); err == nil {
		t.Fatal("expected validation failure, got nil")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "before\n" {
		t.Fatalf("rollback failed, got %q", got)
	}
	items := tracker.List()
	if len(items) == 0 {
		t.Fatal("expected change tracker to record attempted mutation")
	}
	if !strings.Contains(string(got), "before") {
		t.Fatalf("unexpected content after rollback: %q", got)
	}
}
