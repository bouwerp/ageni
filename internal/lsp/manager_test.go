package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManager_ResolvePythonPath(t *testing.T) {
	dir := t.TempDir()

	// 1. No virtual env should fallback to default python3
	p := resolvePythonPath(dir)
	if p != "python3" {
		t.Errorf("expected python3, got %s", p)
	}

	// 2. Create .venv/bin/python
	binDir := filepath.Join(dir, ".venv", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pyFile := filepath.Join(binDir, "python")
	if err := os.WriteFile(pyFile, []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}

	p = resolvePythonPath(dir)
	if p != pyFile {
		t.Errorf("expected %s, got %s", pyFile, p)
	}
}

func TestManager_FindTsdk(t *testing.T) {
	dir := t.TempDir()

	// 1. No node_modules/typescript/lib
	p := findTsdk(dir)
	if p != "" {
		t.Errorf("expected empty string, got %s", p)
	}

	// 2. Create node_modules/typescript/lib
	libDir := filepath.Join(dir, "node_modules", "typescript", "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p = findTsdk(dir)
	if p != libDir {
		t.Errorf("expected %s, got %s", libDir, p)
	}
}
