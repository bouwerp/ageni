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
