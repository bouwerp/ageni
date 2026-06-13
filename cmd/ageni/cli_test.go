package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadPrompt(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		ok       bool
	}{
		{
			name:     "single line",
			input:    "hello world\n",
			expected: "hello world",
			ok:       true,
		},
		{
			name:     "empty line",
			input:    "\n",
			expected: "",
			ok:       true,
		},
		{
			name:     "multiline with backslash",
			input:    "line one \\\nline two \\\nline three\n",
			expected: "line one \nline two \nline three",
			ok:       true,
		},
		{
			name:     "exit command",
			input:    "exit\n",
			expected: "exit",
			ok:       true,
		},
		{
			name:     "quit command",
			input:    "quit\n",
			expected: "quit",
			ok:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scanner := bufio.NewScanner(strings.NewReader(tc.input))
			got, ok := readPrompt(scanner)
			if ok != tc.ok {
				t.Fatalf("expected ok=%v, got %v", tc.ok, ok)
			}
			if got != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}
