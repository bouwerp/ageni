package models

import (
	"strings"
	"unicode"
)

// EstimateComplexity analyses a task objective string and returns a canonical
// tier suggestion ("fast", "mid", "flagship") and a confidence score (0.0–1.0).
//
// The heuristic uses lightweight signal extraction — no LLM calls, no external
// state, deterministic. The caller should treat the returned tier as a default
// floor; the master may override it upward.
//
// Signal table:
//
//	+2   per 200 characters of objective length (long tasks are usually complex)
//	+3   for code fences (```) — multi-block code implies technical synthesis
//	+2   for keywords indicating synthesis/design: "architect", "design", "refactor all"
//	+2   for keywords indicating ambiguity: "how should", "what is the best", "tradeoff"
//	+1   for each file path mention (e.g. "internal/", ".go", ".ts", ".py")
//	+1   for sub-questions (sentences starting with "and", "also", "additionally")
//	-2   for keywords indicating simple retrieval: "find", "list", "show", "grep", "search"
//	-2   for keywords indicating a single-file trivial task: "rename", "fix typo", "add comment"
func EstimateComplexity(objective string) (tier string, score float64) {
	if objective == "" {
		return "mid", 0.5
	}

	lower := strings.ToLower(objective)
	var raw float64

	// Length signal: +2 per 200 chars.
	raw += float64(len(objective)) / 200.0 * 2.0

	// Code fence signal.
	raw += float64(strings.Count(objective, "```")) * 1.5

	// Synthesis / design keywords.
	synthesisKeywords := []string{
		"architect", "redesign", "refactor all", "migrate all", "overhaul",
		"design a", "design the", "implement a full", "end-to-end",
		"what is the best approach", "how should i", "how should we",
		"tradeoff", "trade-off", "strategy", "plan for", "consider",
		"across multiple", "across all",
	}
	for _, kw := range synthesisKeywords {
		if strings.Contains(lower, kw) {
			raw += 2.0
		}
	}

	// Multi-file / multi-step signal: count file path hints.
	fileSignals := []string{
		".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".kt",
		".swift", ".rb", ".php", ".yaml", ".yml", ".json", ".toml",
		"internal/", "src/", "pkg/", "lib/", "cmd/", "test/",
	}
	fileCount := 0
	for _, sig := range fileSignals {
		fileCount += strings.Count(lower, sig)
	}
	if fileCount > 0 {
		raw += min64(float64(fileCount)*0.5, 3.0) // cap at +3
	}

	// Sub-question signal: sentences that extend the task.
	subQuestionWords := []string{" and also ", " additionally ", " furthermore ", " moreover "}
	for _, w := range subQuestionWords {
		raw += float64(strings.Count(lower, w)) * 0.5
	}

	// Simple retrieval keywords reduce complexity.
	retrievalKeywords := []string{
		"find the", "list all", "list the", "show me", "grep for",
		"search for", "where is", "what file", "which file",
	}
	for _, kw := range retrievalKeywords {
		if strings.Contains(lower, kw) {
			raw -= 2.0
		}
	}

	// Trivial task keywords.
	trivialKeywords := []string{
		"fix typo", "add comment", "rename variable", "rename the",
		"remove the", "delete the", "add a blank", "what does",
	}
	for _, kw := range trivialKeywords {
		if strings.Contains(lower, kw) {
			raw -= 2.0
		}
	}

	// Normalise to 0.0–1.0.
	// Raw 0 → 0.5 (mid); raw < -2 → fast; raw > 4 → flagship.
	normalised := (raw + 4.0) / 12.0
	if normalised < 0 {
		normalised = 0
	}
	if normalised > 1 {
		normalised = 1
	}

	switch {
	case normalised < 0.30:
		return "fast", normalised
	case normalised > 0.65:
		return "flagship", normalised
	default:
		return "mid", normalised
	}
}

// ExtractRequiredCaps scans an objective string for keywords that imply
// required model capabilities. Returns a deduplicated slice of capability
// names (e.g. ["vision", "reasoning"]).
//
// Rules:
//
//	image/screenshot/photo/visual → "vision"
//	reason through/think through/multi-step analysis/deep analysis → "reasoning"
func ExtractRequiredCaps(objective string) []string {
	lower := strings.ToLower(objective)
	var caps []string

	visionKeywords := []string{
		"image", "screenshot", "photo", "picture", "visual",
		"diagram", "chart", "figure", "ui screenshot", "render",
	}
	for _, kw := range visionKeywords {
		if strings.Contains(lower, kw) {
			caps = append(caps, "vision")
			break
		}
	}

	reasoningKeywords := []string{
		"reason through", "think through", "multi-step analysis", "deep analysis",
		"step by step reasoning", "chain of thought", "careful reasoning",
		"complex reasoning", "mathematical proof", "formal verification",
	}
	for _, kw := range reasoningKeywords {
		if strings.Contains(lower, kw) {
			caps = append(caps, "reasoning")
			break
		}
	}

	return caps
}

// WordCount returns the number of words in s (Unicode-aware).
func WordCount(s string) int {
	inWord := false
	count := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			inWord = false
		} else if !inWord {
			inWord = true
			count++
		}
	}
	return count
}

// EstimateTokens returns a rough token count estimate for a string.
// Uses the chars/4 rule-of-thumb (slightly conservative vs GPT's ~4 chars/token).
func EstimateTokens(s string) int {
	return max(1, len(s)/4)
}

// EstimateMessagesTokens sums EstimateTokens over all role+content pairs.
func EstimateMessagesTokens(messages []struct{ Content string }) int {
	total := 0
	for _, m := range messages {
		total += EstimateTokens(m.Content) + 4 // role overhead
	}
	return total
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
