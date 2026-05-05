package llm

import "testing"

func TestRankingNormalisation(t *testing.T) {
	cases := []struct {
		in       string
		wantOK   bool
		wantMin  int
	}{
		{"claude-opus-4-7", true, 80},
		{"anthropic/claude-opus-4-7", true, 80}, // strip provider prefix
		{"anthropic/claude-opus-4-7:free", true, 80}, // strip prefix + :free
		{"meta-llama/llama-3.3-70b-instruct:free", true, 40}, // OpenRouter routing
		{"unknown-model", false, 0},
	}
	for _, c := range cases {
		got, ok := RankingFor(c.in)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v", c.in, ok, c.wantOK)
		}
		if c.wantOK && got.Score < c.wantMin {
			t.Errorf("%s: score=%d want >=%d", c.in, got.Score, c.wantMin)
		}
	}
}
