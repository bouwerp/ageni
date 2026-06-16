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

func TestRegistryUnknownToolMessage(t *testing.T) {
	// Import strings package for testing
	// (Note: we can either use strings package or implement basic check, let's use strings)
	r1 := NewRegistry()
	r1.Register(dummyTool{name: "glob"})
	msg1 := r1.unknownToolMessage("some_tool")
	if len(msg1) == 0 {
		t.Errorf("Expected non-empty message")
	}
	// Check if "use run_bash" is in the message
	found := false
	for _, line := range []string{"use run_bash", "run_bash"} {
		if stringsContains(msg1, line) {
			found = true
		}
	}
	if found {
		t.Errorf("Expected msg1 to NOT contain run_bash suggestion, got:\n%s", msg1)
	}

	// 2. With run_bash registered
	r2 := NewRegistry()
	r2.Register(dummyTool{name: "run_bash"})
	msg2 := r2.unknownToolMessage("some_tool")
	found2 := false
	for _, line := range []string{"use run_bash", "run_bash"} {
		if stringsContains(msg2, line) {
			found2 = true
		}
	}
	if !found2 {
		t.Errorf("Expected msg2 to contain run_bash suggestion, got:\n%s", msg2)
	}
}

// Simple string helper to avoid importing strings package if not needed, or we can import it.
// Wait, main/registry_test.go does not import strings yet.
func stringsContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
