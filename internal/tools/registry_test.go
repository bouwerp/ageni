package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type dummyTool struct {
	name string
}

func (d dummyTool) Name() string           { return d.name }
func (d dummyTool) Description() string    { return "dummy" }
func (d dummyTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (d dummyTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	return "ok", nil
}

func TestRegistryAliases(t *testing.T) {
	r := NewRegistry()
	r.Register(dummyTool{name: "run_bash"})
	r.Register(dummyTool{name: "glob"})

	// Test Get with alias.
	tool, ok := r.Get("bash_tool")
	if !ok || tool.Name() != "run_bash" {
		t.Fatalf("expected bash_tool to resolve to run_bash, got ok=%v, tool=%v", ok, tool)
	}

	// Test Missing with alias.
	missing := r.Missing([]string{"bash_tool", "glob_search", "unknown_tool"})
	if len(missing) != 1 || missing[0] != "unknown_tool" {
		t.Fatalf("expected only unknown_tool to be missing, got: %v", missing)
	}

	// Test Subset with alias.
	sub := r.Subset([]string{"bash_tool", "glob_search"})
	if _, ok := sub.Get("run_bash"); !ok {
		t.Fatalf("expected run_bash to be in subset")
	}
	if _, ok := sub.Get("glob"); !ok {
		t.Fatalf("expected glob to be in subset")
	}
}
