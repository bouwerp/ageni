package models

import "testing"

func TestSeedRanked(t *testing.T) {
	ranked := Global.Ranked()
	if len(ranked) == 0 {
		t.Fatal("registry is empty")
	}
	top := ranked[0]
	if top.BlendedScore <= 0 {
		t.Fatalf("top model has no score: %s %.1f", top.ID, top.BlendedScore)
	}
	t.Logf("Top 5 models:")
	for i, m := range ranked {
		if i >= 5 {
			break
		}
		t.Logf("  %d. %s (%.1f) aider=%.1f seed=%.1f",
			i+1, m.Name, m.BlendedScore, m.Scores["aider_polyglot"], m.Scores["seed_blended"])
	}
}

func TestNormaliseAiderName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gpt-5 (high)", "gpt-5"},
		{"claude-opus-4-20250514", "claude-opus-4"},
		{"gemini-2.5-flash-preview-05-20", "gemini-2.5-flash-preview"},
		{"DeepSeek R1 (0528)", "deepseek r1"},
		{"gpt-4o-2024-11-20", "gpt-4o"},
	}
	for _, c := range cases {
		got := normaliseAiderName(c.in)
		if got != c.want {
			t.Errorf("normaliseAiderName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseAiderYAML(t *testing.T) {
sample := []byte(`
- dirname: test-1
  model: gpt-5 (high)
  pass_rate_1: 70.0
  pass_rate_2: 88.0
- dirname: test-2
  model: claude-opus-4-20250514
  pass_rate_1: 55.0
  pass_rate_2: 72.0
- dirname: test-3
  model: deepseek-chat
  pass_rate_1: 40.0
  pass_rate_2: 62.0
`)
scores := parseAiderYAML(sample)
cases := map[string]float64{
"gpt-5":              88.0,
"claude-opus-4":      72.0,
"deepseek-chat":      62.0,
}
for name, want := range cases {
if got, ok := scores[name]; !ok || got != want {
t.Errorf("score[%q] = %.1f, want %.1f (found=%v)", name, got, want, ok)
}
}
}
