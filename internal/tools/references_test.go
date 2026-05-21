package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindGoReferences(t *testing.T) {
	dir := t.TempDir()
	src := `package sample

func Target() {}

func call() {
	Target()
}`
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	out, err := (FindReferences{}).Call(context.Background(), json.RawMessage(`{"symbol":"Target"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, "sample.go:3:6") || !strings.Contains(out, "sample.go:6:2") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestSearchSymbolsPrefersGoASTMatches(t *testing.T) {
	dir := t.TempDir()
	src := `package sample

type Target struct{}

func call() {
	_ = Target{}
}`
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	out, err := (SearchSymbols{}).Call(context.Background(), json.RawMessage(`{"query":"Target"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, "sample.go:3  type Target") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}
