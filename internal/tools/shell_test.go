package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bouwerp/ageni/internal/llm"
)

func TestRunBashCollapsesBlankLinesAndTruncates(t *testing.T) {
	tool := RunBash{}
	args, _ := json.Marshal(map[string]any{
		"command": `for i in $(seq 1 220); do
  printf 'line-%03d\n' "$i"
  if [ "$i" = "3" ]; then printf '\n\n\n\n'; fi
done`,
	})
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("RunBash: %v", err)
	}
	if strings.Contains(out, "\n\n\n\n") {
		t.Fatalf("expected blank lines to be collapsed, got: %q", out)
	}
	if !strings.Contains(out, "[truncated to 160 lines]") {
		t.Fatalf("expected line truncation notice, got: %s", out)
	}
}

func TestRegistryShellAlias(t *testing.T) {
	r := NewRegistry()
	r.Register(RunBash{})

	call := llm.ToolCall{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command": "echo hello"}`),
	}
	res := r.Execute(context.Background(), call)
	if res.IsError {
		t.Fatalf("expected successful execution, got error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "hello") {
		t.Fatalf("expected output to contain 'hello', got: %q", res.Content)
	}
}

func TestRegistryGuessAlternative(t *testing.T) {
	r := NewRegistry()
	
	// Test without run_bash registered
	call := llm.ToolCall{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command": "echo hello"}`),
	}
	res := r.Execute(context.Background(), call)
	if !res.IsError {
		t.Fatalf("expected error for unknown tool")
	}
	if !strings.Contains(res.Content, "Did you mean run_bash") {
		t.Fatalf("expected content to contain hint, got: %q", res.Content)
	}
}

