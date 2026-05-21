package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetGetDelete(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{items: map[string]*Memory{}, writeAt: dir}
	r.rebuildOrder()

	if err := r.Set("test-key", "A test memory", "Some content here."); err != nil {
		t.Fatalf("Set: %v", err)
	}

	m := r.Get("test-key")
	if m == nil {
		t.Fatal("Get returned nil after Set")
	}
	if m.Description != "A test memory" {
		t.Errorf("Description = %q, want %q", m.Description, "A test memory")
	}
	if m.Content != "Some content here." {
		t.Errorf("Content = %q, want %q", m.Content, "Some content here.")
	}

	// File must exist.
	if _, err := os.Stat(filepath.Join(dir, "test-key.md")); err != nil {
		t.Errorf("backing file missing: %v", err)
	}

	// Delete.
	if err := r.Delete("test-key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if r.Get("test-key") != nil {
		t.Error("Get returned non-nil after Delete")
	}
	if _, err := os.Stat(filepath.Join(dir, "test-key.md")); !os.IsNotExist(err) {
		t.Error("backing file still present after Delete")
	}
}

func TestInlineBlock(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{items: map[string]*Memory{}, writeAt: dir}
	r.rebuildOrder()

	// Empty registry → empty block.
	if block := r.InlineBlock(); block != "" {
		t.Errorf("InlineBlock with no memories = %q, want empty", block)
	}

	if err := r.Set("build", "How to build", "Run go build ./..."); err != nil {
		t.Fatal(err)
	}

	block := r.InlineBlock()
	if !strings.Contains(block, "<memories>") {
		t.Error("InlineBlock missing <memories> tag")
	}
	if !strings.Contains(block, "build") {
		t.Error("InlineBlock missing key 'build'")
	}
	if !strings.Contains(block, "Run go build ./...") {
		t.Error("InlineBlock missing content")
	}
}

func TestInlineBlockForQuerySelectsRelevantMemories(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{items: map[string]*Memory{}, writeAt: dir}
	r.rebuildOrder()

	if err := r.Set("build", "Build instructions", "Run go build ./..."); err != nil {
		t.Fatal(err)
	}
	if err := r.Set("auth", "Authentication notes", "JWT middleware lives in internal/auth"); err != nil {
		t.Fatal(err)
	}
	if err := r.Set("release", "Release process", "Push a v*.*.* tag to trigger the workflow"); err != nil {
		t.Fatal(err)
	}

	block := r.InlineBlockForQuery("build the binary and fix build failures", 1)
	if !strings.Contains(block, "Showing 1 of 3 memories") {
		t.Fatalf("expected filtered memory notice, got %q", block)
	}
	if !strings.Contains(block, "**build**") {
		t.Fatalf("expected build memory to be selected, got %q", block)
	}
	if strings.Contains(block, "**auth**") || strings.Contains(block, "**release**") {
		t.Fatalf("expected only the relevant memory, got %q", block)
	}
}

func TestInlineBlockForQueryFallsBackWhenNoMatches(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{items: map[string]*Memory{}, writeAt: dir}
	r.rebuildOrder()

	if err := r.Set("build", "Build instructions", "Run go build ./..."); err != nil {
		t.Fatal(err)
	}
	if err := r.Set("release", "Release process", "Push a v*.*.* tag to trigger the workflow"); err != nil {
		t.Fatal(err)
	}

	block := r.InlineBlockForQuery("calendar widget colors", 1)
	if !strings.Contains(block, "**build**") || !strings.Contains(block, "**release**") {
		t.Fatalf("expected fallback to the full memory block when no memories match, got %q", block)
	}
}

func TestParseFrontmatter(t *testing.T) {
	input := "---\ndescription: my description\n---\nmy content\n"
	desc, body := parseFrontmatter(input)
	if desc != "my description" {
		t.Errorf("description = %q", desc)
	}
	if !strings.Contains(body, "my content") {
		t.Errorf("body = %q", body)
	}

	// No frontmatter.
	desc2, body2 := parseFrontmatter("just plain text")
	if desc2 != "" {
		t.Errorf("expected empty desc, got %q", desc2)
	}
	if body2 != "just plain text" {
		t.Errorf("body2 = %q", body2)
	}
}

func TestLoadFrom(t *testing.T) {
	dir := t.TempDir()
	// Write a memory file manually.
	content := "---\ndescription: test desc\n---\ntest body\n"
	if err := os.WriteFile(filepath.Join(dir, "mykey.md"), []byte(content), 0640); err != nil {
		t.Fatal(err)
	}

	r := &Registry{items: map[string]*Memory{}, writeAt: dir}
	if err := r.loadFrom(dir); err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	r.rebuildOrder()

	m := r.Get("mykey")
	if m == nil {
		t.Fatal("loadFrom did not load mykey")
	}
	if m.Description != "test desc" {
		t.Errorf("Description = %q", m.Description)
	}
	if m.Content != "test body" {
		t.Errorf("Content = %q", m.Content)
	}
}
