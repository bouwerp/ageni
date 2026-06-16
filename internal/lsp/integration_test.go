package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLSP_RealGoplsIntegration(t *testing.T) {
	// 1. Check if gopls is installed on the system
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed, skipping real integration test")
	}

	// 2. Create a temporary Go workspace
	dir := t.TempDir()

	// Create go.mod
	goModContent := "module testmod\n\ngo 1.18\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goModContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create main.go defining a function
	mainContent := `package main

import "fmt"

func SayHello(name string) {
	fmt.Println("Hello, " + name)
}

func main() {
	SayHello("World")
}
`
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(mainContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. Initialize the Manager
	manager := &Manager{
		clients:     make(map[string]*Client),
		openFiles:   make(map[string]int),
		diagnostics: make(map[string][]Diagnostic),
	}
	manager.Init(dir)
	defer manager.CloseAll()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 4. Open the file to notify gopls
	if err := manager.OpenFile(ctx, mainPath, mainContent); err != nil {
		t.Fatalf("failed to open file in gopls: %v", err)
	}

	// Give gopls a moment to analyze the workspace
	time.Sleep(1 * time.Second)

	// 5. Test GetDefinition: find definition of SayHello at line 10 (0-indexed line 9, character 2)
	locs, err := manager.GetDefinition(ctx, mainPath, 9, 2)
	if err != nil {
		t.Fatalf("GetDefinition failed: %v", err)
	}

	if len(locs) == 0 {
		t.Fatal("expected at least one definition location")
	}

	// Definition should point to `func SayHello(name string)` at line 5 (0-indexed line 4)
	foundDef := false
	for _, loc := range locs {
		if strings.HasSuffix(loc.URI, "main.go") && loc.Range.Start.Line == 4 {
			foundDef = true
			break
		}
	}
	if !foundDef {
		t.Errorf("definition not found at line 5, got locations: %+v", locs)
	}

	// 6. Test GetHover at line 10
	hoverText, err := manager.GetHover(ctx, mainPath, 9, 2)
	if err != nil {
		t.Fatalf("GetHover failed: %v", err)
	}
	if !strings.Contains(hoverText, "func SayHello") {
		t.Errorf("expected hover text to contain 'func SayHello', got: %q", hoverText)
	}

	// 7. Test GetReferences: find references to SayHello
	refs, err := manager.GetReferences(ctx, mainPath, 4, 5) // at declaration: `func SayHello`
	if err != nil {
		t.Fatalf("GetReferences failed: %v", err)
	}
	// Should find both the declaration (line 5 / index 4) and the call (line 10 / index 9)
	if len(refs) < 2 {
		t.Errorf("expected at least 2 references, got %d: %+v", len(refs), refs)
	}
}

func TestLSP_MissingExecutableFallback(t *testing.T) {
	dir := t.TempDir()
	manager := &Manager{
		clients:     make(map[string]*Client),
		openFiles:   make(map[string]int),
		diagnostics: make(map[string][]Diagnostic),
	}
	manager.Init(dir)
	defer manager.CloseAll()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := manager.GetClient(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty language")
	}
	if !strings.Contains(err.Error(), "unsupported language") {
		t.Errorf("unexpected error message for empty lang: %v", err)
	}

	_, err2 := manager.GetClient(ctx, "unsupported_lang_123")
	if err2 == nil {
		t.Fatal("expected error for unsupported language")
	}
	if !strings.Contains(err2.Error(), "no language server executable found") {
		t.Errorf("unexpected error message for unsupported lang: %v", err2)
	}
}
