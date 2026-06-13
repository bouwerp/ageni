package agent

import (
	"strings"
	"testing"

	"github.com/bouwerp/ageni/internal/llm"
)

func TestThinkStripper(t *testing.T) {
	tests := []struct {
		name     string
		inputs   []string
		expected string
	}{
		{
			name:     "no thinking tags",
			inputs:   []string{"hello ", "world"},
			expected: "hello world",
		},
		{
			name:     "simple thinking tag",
			inputs:   []string{"<think>thinking inside</think>hello ", "world"},
			expected: "hello world",
		},
		{
			name:     "split thinking tags",
			inputs:   []string{"<th", "ink>thinking inside</th", "ink>hello ", "world"},
			expected: "hello world",
		},
		{
			name:     "thinking tag at end",
			inputs:   []string{"hello ", "world<think>thinking</think>"},
			expected: "hello world",
		},
		{
			name:     "thinking tag with partial suffix",
			inputs:   []string{"hello ", "world<th"},
			expected: "hello world<th",
		},
		{
			name:     "thinking tag with partial suffix inside think",
			inputs:   []string{"<think>thinking", "</th"},
			expected: "",
		},
		{
			name:     "thinking tag open and never closed",
			inputs:   []string{"hello ", "<think>thinking"},
			expected: "hello ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := &thinkStripper{}
			var got string
			for _, in := range tc.inputs {
				got += ts.Feed(in)
			}
			got += ts.Flush()
			if got != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestTrimSubagentHistory(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Text: "Initial task description"},
		{Role: llm.RoleAssistant, Text: "First assistant text"},
		{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{Content: "Tool output 1"}}},
		{Role: llm.RoleAssistant, Text: "Second assistant text"},
		{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{Content: "Tool output 2"}}},
		{Role: llm.RoleAssistant, Text: "Third assistant text"},
		{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{Content: "Tool output 3"}}},
	}

	// If we keep last 2 turns, we should drop the first turn (First assistant text + Tool output 1).
	// So it should keep:
	// - messages[0]
	// - Notice message
	// - messages[3:] (Second assistant text, Tool output 2, Third assistant text, Tool output 3)
	trimmed := trimSubagentHistory(messages, 2)
	if len(trimmed) != 6 {
		t.Fatalf("expected length 6, got %d", len(trimmed))
	}
	if trimmed[0].Text != "Initial task description" {
		t.Errorf("expected initial task description at index 0, got %q", trimmed[0].Text)
	}
	if !strings.Contains(trimmed[1].Text, "removed to fit local model context window") {
		t.Errorf("expected notice at index 1, got %q", trimmed[1].Text)
	}
	if trimmed[2].Text != "Second assistant text" {
		t.Errorf("expected second assistant text at index 2, got %q", trimmed[2].Text)
	}
	if trimmed[4].Text != "Third assistant text" {
		t.Errorf("expected third assistant text at index 4, got %q", trimmed[4].Text)
	}
}

