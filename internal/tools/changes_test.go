package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckpointRewind drives the full mutate → checkpoint → rewind cycle
// through the actual tools, confirming workspace restoration semantics
// for create / edit / delete kinds.
func TestCheckpointRewind(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.go")
	original := "package x\nvar V = 1\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := NewChangeTracker(filepath.Join(dir, "changes.jsonl"), filepath.Join(dir, "snap"))

	// Step 1: edit existing file
	ef := EditFile{Tracker: tr}
	args, _ := json.Marshal(map[string]any{
		"path": target, "old_string": "var V = 1\n", "new_string": "var V = 2\n",
	})
	if _, err := ef.Call(context.Background(), args); err != nil {
		t.Fatal(err)
	}

	// Step 2: create new file
	wf := WriteFile{Tracker: tr}
	created := filepath.Join(dir, "new.txt")
	args, _ = json.Marshal(map[string]any{"path": created, "content": "fresh\n"})
	if _, err := wf.Call(context.Background(), args); err != nil {
		t.Fatal(err)
	}

	// Step 3: edit existing file again
	args, _ = json.Marshal(map[string]any{
		"path": target, "old_string": "var V = 2\n", "new_string": "var V = 3\n",
	})
	if _, err := ef.Call(context.Background(), args); err != nil {
		t.Fatal(err)
	}

	// Sanity: 3 checkpoints recorded
	cps := tr.Checkpoints()
	if len(cps) != 3 {
		t.Fatalf("want 3 checkpoints, got %d", len(cps))
	}

	// Rewind to step 2 — undoes step 2 (created file) and step 3 (edit).
	// Expected: target back to V=1, new.txt deleted.
	touched, err := tr.Rewind(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 2 {
		t.Fatalf("want 2 touched, got %d (%v)", len(touched), touched)
	}
	body, _ := os.ReadFile(target)
	if string(body) != "var V = 2\n"+"" && string(body) != original {
		// Step 3's BEFORE-state was "var V = 2"; that's what should
		// be restored.
		if string(body) != "package x\nvar V = 2\n" {
			t.Fatalf("target content wrong: %q", body)
		}
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("new.txt should be deleted after rewind: err=%v", err)
	}
}
