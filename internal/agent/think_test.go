package agent

import (
	"testing"
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
