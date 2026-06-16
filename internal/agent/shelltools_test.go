package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestShellToolsResolveID(t *testing.T) {
	sm := NewShellManager(nil)
	defer sm.CancelAll()

	// Open a shell to get a valid ID.
	s, err := sm.Open("test-shell", ShellKindTask)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	execTool := ShellExecTool{SM: sm}

	// Test with shell_id alias.
	args, _ := json.Marshal(map[string]any{
		"shell_id": s.ID(),
		"command":  "echo hello",
	})
	_, err = execTool.Call(context.Background(), args)
	if err != nil {
		if strings.Contains(err.Error(), "no such shell") {
			t.Fatalf("Failed to resolve shell_id: %v", err)
		}
	}

	// Test with session_id alias.
	args2, _ := json.Marshal(map[string]any{
		"session_id": s.ID(),
		"command":    "echo hello",
	})
	_, err = execTool.Call(context.Background(), args2)
	if err != nil && strings.Contains(err.Error(), "no such shell") {
		t.Fatalf("Failed to resolve session_id: %v", err)
	}
}

func TestShellToolsFallbackAndAutoOpen(t *testing.T) {
	sm := NewShellManager(nil)
	defer sm.CancelAll()

	execTool := ShellExecTool{SM: sm}

	// 1. Test auto-opening when no shells exist
	args, _ := json.Marshal(map[string]any{
		"command": "echo auto-open",
	})
	output, err := execTool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("Failed to auto-open default shell on execution: %v", err)
	}
	if !strings.Contains(output, "auto-open") {
		t.Errorf("Unexpected output: %s", output)
	}

	// Verify one shell was opened
	if sm.Count() != 1 {
		t.Errorf("Expected exactly 1 shell opened, got %d", sm.Count())
	}

	// Get the active shell ID
	var activeID string
	for _, sh := range sm.List() {
		if sh.Status() == ShellStatusOpen {
			activeID = sh.ID()
		}
	}
	if activeID == "" {
		t.Fatalf("No active shell found")
	}

	// 2. Test fallback when exactly one active shell is running
	args2, _ := json.Marshal(map[string]any{
		"command": "echo fallback",
	})
	output2, err := execTool.Call(context.Background(), args2)
	if err != nil {
		t.Fatalf("Failed to fallback to active shell: %v", err)
	}
	if !strings.Contains(output2, "fallback") {
		t.Errorf("Unexpected output: %s", output2)
	}

	// Verify no new shells were opened (still only 1 total shell)
	if sm.Count() != 1 {
		t.Errorf("Expected still only 1 shell, got %d", sm.Count())
	}
}
