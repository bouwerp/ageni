package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/bouwerp/ageni/internal/llm"
)

// Context pressure thresholds. Warning fires at contextWarnPct; compaction
// fires at contextCompactPct (validated: opencode triggers at ~80%, hermes at
// 50%, crush at "20K tokens remaining" which is ~85% on 128K models).
const (
	contextWarnPct    = 70 // warn the user via EvFlash at this % of context used
	contextCompactPct = 80 // trigger LLM compaction at this % of context used

	// compactionTailChars is how many characters of recent message history
	// are preserved verbatim after compaction (approximately 20K tokens at
	// 4 chars/token — matches pi-mono's keepRecentTokens=20000).
	compactionTailChars = 80_000

	// compactionSummaryMaxTokens caps the LLM's summarisation output so it
	// doesn't balloon back to the size we just shrank (pi-mono: 0.8 × 16K ≈ 13K).
	compactionSummaryMaxTokens = 4096
)

// checkContextPressure is called after every master LLM call with the
// returned usage. It:
//   - emits an EvFlash warning when ≥ contextWarnPct of the context window is used
//   - triggers in-place compaction when ≥ contextCompactPct is used
func (m *Master) checkContextPressure(ctx context.Context, usage *llm.Usage) {
	m.mu.RLock()
	cw := m.contextWindow
	m.mu.RUnlock()

	if cw <= 0 || usage == nil {
		return
	}

	used := usage.InputTokens
	pct := used * 100 / cw

	if pct >= contextCompactPct {
		m.bus.Publish(Event{Kind: EvFlash, Text: fmt.Sprintf(
			"⚠ Context at %d%% (%d/%d tokens) — running compaction…", pct, used, cw)})
		if err := m.compact(ctx); err != nil {
			m.bus.Publish(Event{Kind: EvFlash, Text: "compaction error: " + err.Error()})
		}
		return
	}

	if pct >= contextWarnPct {
		m.bus.Publish(Event{Kind: EvFlash, Text: fmt.Sprintf(
			"⚠ Context at %d%% (%d/%d tokens). Consider starting a new session soon or compaction will trigger automatically at %d%%.",
			pct, used, cw, contextCompactPct)})
	}
}

// compact summarises the oldest portion of m.messages with an LLM call and
// replaces those messages with a single compact summary message. The most
// recent compactionTailChars of content are preserved verbatim.
func (m *Master) compact(ctx context.Context) error {
	msgs := m.messages

	// Strip the self-replacing activeContext tail block — we'll re-add it on
	// the next takeTurns invocation via refreshActiveContext.
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role == llm.RoleUser && strings.HasPrefix(last.Text, activeContextMarker) {
			msgs = msgs[:len(msgs)-1]
		} else {
			break
		}
	}

	// Walk backwards from end accumulating char counts until we've covered
	// compactionTailChars — that's the tail we preserve verbatim.
	accumulated := 0
	cutPoint := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		accumulated += msgCharCount(msgs[i])
		if accumulated >= compactionTailChars {
			cutPoint = i + 1
			break
		}
		cutPoint = i
	}

	// Need at least 4 messages to summarise — anything fewer isn't worth it.
	if cutPoint < 4 {
		return nil
	}

	// Ensure the cut never falls in the middle of a tool_call + tool_result
	// pair (claw-code pattern — prevents API 400 errors on providers that
	// validate message ordering).
	cutPoint = safeCutPoint(msgs, cutPoint)
	if cutPoint < 1 {
		return nil
	}

	toSummarise := msgs[:cutPoint]
	tail := msgs[cutPoint:]

	summary, err := m.summariseMessages(ctx, toSummarise)
	if err != nil {
		return err
	}

	summaryMsg := llm.Message{
		Role: llm.RoleUser,
		Text: "[CONTEXT COMPACTION — REFERENCE ONLY]\n" +
			"The following is a summary of the conversation history that was compacted to free context space. " +
			"Use it as reference; the live conversation continues after this block.\n\n" +
			summary,
	}

	newMsgs := make([]llm.Message, 0, 1+len(tail))
	newMsgs = append(newMsgs, summaryMsg)
	newMsgs = append(newMsgs, tail...)
	m.messages = newMsgs

	m.bus.Publish(Event{Kind: EvFlash, Text: fmt.Sprintf(
		"✓ Compaction complete: summarised %d messages, kept %d recent messages.", cutPoint, len(tail))})
	return nil
}

// safeCutPoint adjusts cut so it never splits an assistant tool_call message
// from the tool_result message(s) that follow it, and never ends on an
// assistant message that has pending tool calls.
func safeCutPoint(msgs []llm.Message, cut int) int {
	for cut > 0 {
		// The message immediately before cut must be a valid boundary.
		// Bad cuts:
		//   - cut falls on a tool-result message (its tool_call is at cut-1)
		//   - cut falls right after an assistant message that has tool calls
		//     (the results are at cut, inside the tail)
		if cut > 0 && msgs[cut-1].Role == llm.RoleAssistant && len(msgs[cut-1].ToolCalls) > 0 {
			// The tool results for this assistant message would be split into
			// the tail — walk cut back one more step.
			cut--
			continue
		}
		if msgs[cut-1].Role == llm.RoleTool {
			// Never end on a tool result — its paired assistant call must also
			// be in the summarised portion.
			cut--
			continue
		}
		break
	}
	return cut
}

// summariseMessages calls the master's adapter to produce a structured
// summary of the supplied messages. Uses a compact prompt to keep the
// summary tight — the template follows opencode / pi-mono's format.
func (m *Master) summariseMessages(ctx context.Context, msgs []llm.Message) (string, error) {
	if len(msgs) == 0 {
		return "", nil
	}

	// Build a plain-text transcript of the messages to summarise. Tool
	// results are capped at 500 chars each so the summarisation prompt
	// itself doesn't exceed the context limit.
	var sb strings.Builder
	for _, msg := range msgs {
		switch msg.Role {
		case llm.RoleUser:
			if !strings.HasPrefix(msg.Text, activeContextMarker) {
				sb.WriteString("USER: " + msg.Text + "\n\n")
			}
		case llm.RoleAssistant:
			if msg.Text != "" {
				sb.WriteString("ASSISTANT: " + msg.Text + "\n\n")
			}
			for _, tc := range msg.ToolCalls {
				sb.WriteString(fmt.Sprintf("TOOL_CALL(%s): %s\n\n", tc.Name, string(tc.Arguments)))
			}
		case llm.RoleTool:
			for _, tr := range msg.ToolResults {
				content := tr.Content
				if len(content) > 500 {
					content = content[:500] + "…[truncated]"
				}
				sb.WriteString(fmt.Sprintf("TOOL_RESULT: %s\n\n", content))
			}
		}
	}

	adapter, model := m.adapterForIter(0)
	req := llm.Request{
		Model:     model,
		MaxTokens: compactionSummaryMaxTokens,
		System: `You are a context compaction assistant. Produce a concise structured summary of the conversation segment provided. Follow this template exactly:

## Goal
One sentence: what the user asked for.

## Constraints & Preferences
Bullet list of any explicit constraints or preferences the user stated.

## Progress
### Done
- Completed actions with file paths and key outcomes.
### In Progress
- Actions underway or partially done.
### Blocked
- Anything blocked and why.

## Key Decisions
- Architectural or design decisions made, with rationale.

## Relevant Files
- path/to/file.go: what was done to it / why it matters

## Next Steps
- What remains to be done.

## Critical Context
Any facts that must not be lost (error messages, exact values, user corrections).

Be precise and brief. Omit pleasantries and filler. Every bullet must be actionable or informative.`,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "Summarise the following conversation segment:\n\n" + sb.String()},
		},
	}

	stream, err := adapter.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("compact: %w", err)
	}

	var result strings.Builder
	for ev := range stream {
		switch ev.Type {
		case llm.StreamEventText:
			result.WriteString(ev.TextDelta)
		case llm.StreamEventError:
			return "", fmt.Errorf("compact stream: %w", ev.Err)
		}
	}
	return result.String(), nil
}

// msgCharCount estimates the character count of a message for compaction
// boundary calculations. Tool results are included at full length.
func msgCharCount(m llm.Message) int {
	n := len(m.Text)
	for _, tc := range m.ToolCalls {
		n += len(tc.Name) + len(tc.Arguments)
	}
	for _, tr := range m.ToolResults {
		n += len(tr.Content)
	}
	return n
}
