package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bouwerp/ageni/internal/llm"
)

// LoadHistory reads the session log and reconstructs the master's prior
// message buffer so a resumed session continues with full conversational
// memory of every user message, assistant turn, tool call, and tool
// result it produced.
//
// Sub-agent events are NOT replayed directly — the master's view of
// sub-agents was always mediated through master_tool_call/done pairs
// (spawn_subagent / check_subagent), and those tool results contain
// whatever the master "saw" of the workers. So the sub-agents themselves
// don't come back, but the master's memory of what they returned does.
//
// Tool-call IDs are minted fresh ("replay-N") and applied consistently
// to call + result pairs so the LLM API accepts the seeded history.
func LoadHistory(s *Session) ([]llm.Message, error) {
	f, err := os.Open(s.Path("log.jsonl")) //nolint:gosec
	if err != nil {
		// Missing log = nothing to replay; not an error for fresh sessions.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var msgs []llm.Message
	var assistantText strings.Builder
	var pendingCalls []llm.ToolCall
	var toolCallIDs []string
	toolDoneIdx := 0
	assistantEmitted := false
	nextID := 0

	resetIter := func() {
		assistantText.Reset()
		pendingCalls = nil
		toolCallIDs = nil
		toolDoneIdx = 0
		assistantEmitted = false
	}

	emitAssistant := func() {
		if assistantEmitted {
			return
		}
		// Skip empty assistant messages (no text, no tool calls). These
		// occur if a turn was cancelled before it produced anything.
		if assistantText.Len() == 0 && len(pendingCalls) == 0 {
			assistantEmitted = true
			return
		}
		msgs = append(msgs, llm.Message{
			Role:      llm.RoleAssistant,
			Text:      assistantText.String(),
			ToolCalls: pendingCalls,
		})
		assistantEmitted = true
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e logEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		switch e.Kind {
		case "user_message":
			emitAssistant()
			resetIter()
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: e.Text})
		case "master_text":
			// Seeing text after the assistant message was already emitted
			// means a new iteration is starting (post-tool-result).
			if assistantEmitted {
				resetIter()
			}
			assistantText.WriteString(e.Text)
		case "master_tool_call":
			if assistantEmitted {
				resetIter()
			}
			nextID++
			id := fmt.Sprintf("replay-%d", nextID)
			toolCallIDs = append(toolCallIDs, id)
			pendingCalls = append(pendingCalls, llm.ToolCall{
				ID:        id,
				Name:      e.ToolName,
				Arguments: json.RawMessage(e.ToolArgs),
			})
		case "master_tool_done":
			// First tool_done after a batch of tool_calls finalises the
			// assistant message that issued them.
			emitAssistant()
			id := ""
			if toolDoneIdx < len(toolCallIDs) {
				id = toolCallIDs[toolDoneIdx]
				toolDoneIdx++
			}
			msgs = append(msgs, llm.Message{
				Role: llm.RoleTool,
				ToolResults: []llm.ToolResult{{
					ToolCallID: id,
					Content:    e.ToolResult,
					IsError:    e.ToolError,
				}},
			})
		case "master_turn_done":
			emitAssistant()
			resetIter()
		}
	}
	emitAssistant()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	return compactReplayHistory(msgs), nil
}

const replayKeepExchanges = 3

func compactReplayHistory(msgs []llm.Message) []llm.Message {
	exchanges := 0
	for _, msg := range msgs {
		if msg.Role == llm.RoleUser {
			exchanges++
		}
	}
	if exchanges <= replayKeepExchanges {
		return msgs
	}
	keepFrom := replayKeepBoundary(msgs)
	if keepFrom <= 0 || keepFrom >= len(msgs) {
		return msgs
	}
	compacted := buildReplayCompactedContext(msgs[:keepFrom], msgs[keepFrom:])
	if compacted == "" {
		return msgs
	}
	return append([]llm.Message{{Role: llm.RoleUser, Text: compacted}}, msgs[keepFrom:]...)
}

func replayKeepBoundary(msgs []llm.Message) int {
	exchanges := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleUser {
			continue
		}
		exchanges++
		if exchanges <= replayKeepExchanges {
			continue
		}
		for j := i + 1; j < len(msgs); j++ {
			if msgs[j].Role == llm.RoleUser {
				return j
			}
		}
	}
	return 0
}

func buildReplayCompactedContext(older, recent []llm.Message) string {
	const maxSectionItems = 6
	decisions := make([]string, 0, maxSectionItems)
	completed := make([]string, 0, maxSectionItems)
	pending := make([]string, 0, maxSectionItems)
	artifacts := make([]string, 0, maxSectionItems)
	seenArtifacts := map[string]bool{}

	addUnique := func(dst *[]string, value string) {
		value = strings.TrimSpace(value)
		if value == "" || len(*dst) >= maxSectionItems {
			return
		}
		for _, existing := range *dst {
			if existing == value {
				return
			}
		}
		*dst = append(*dst, value)
	}

	for _, msg := range older {
		switch msg.Role {
		case llm.RoleUser:
			trimmed := strings.TrimSpace(msg.Text)
			if trimmed == "" || strings.HasPrefix(trimmed, "<system-reminder>") || strings.HasPrefix(trimmed, "<session-resume>") {
				continue
			}
			addUnique(&decisions, "User asked: "+replayClip(trimmed, 180))
			for _, match := range replayArtifactRE.FindAllString(trimmed, -1) {
				if !seenArtifacts[match] {
					seenArtifacts[match] = true
					addUnique(&artifacts, match)
				}
			}
			if replayLooksPending(trimmed) {
				addUnique(&pending, replayClip(trimmed, 180))
			}
		case llm.RoleAssistant:
			trimmed := strings.TrimSpace(msg.Text)
			if trimmed != "" {
				addUnique(&decisions, "Assistant concluded: "+replayClip(trimmed, 180))
				if replayLooksPending(trimmed) {
					addUnique(&pending, replayClip(trimmed, 180))
				}
			}
			for _, tc := range msg.ToolCalls {
				addUnique(&completed, "Tool call: "+tc.Name)
			}
		case llm.RoleTool:
			for _, tr := range msg.ToolResults {
				label := "Tool result: "
				if tr.IsError {
					label = "Tool error: "
				}
				addUnique(&completed, label+replayClip(tr.Content, 180))
				for _, match := range replayArtifactRE.FindAllString(tr.Content, -1) {
					if !seenArtifacts[match] {
						seenArtifacts[match] = true
						addUnique(&artifacts, match)
					}
				}
				if replayLooksPending(tr.Content) {
					addUnique(&pending, replayClip(tr.Content, 180))
				}
			}
		}
	}

	recentExchanges := 0
	for _, msg := range recent {
		if msg.Role == llm.RoleUser {
			recentExchanges++
		}
	}

	var sb strings.Builder
	sb.WriteString(`<compacted_context source="replay">` + "\n")
	sb.WriteString("<summary>")
	sb.WriteString(html.EscapeString(fmt.Sprintf(
		"Resumed session with %d older message(s) compacted from the session log. The %d most recent exchange(s) remain verbatim below.",
		len(older), recentExchanges,
	)))
	sb.WriteString("</summary>\n")
	writeReplaySection(&sb, "decisions", decisions)
	writeReplaySection(&sb, "completed", completed)
	writeReplaySection(&sb, "pending", pending)
	writeReplaySection(&sb, "artifacts", artifacts)
	sb.WriteString("</compacted_context>")
	return sb.String()
}

var replayArtifactRE = regexp.MustCompile(`(?:[A-Za-z0-9_.\-/]+\.[A-Za-z0-9_]+(?::\d+)?)|(?:s\d+|sh\d+|replay-\d+|v\d+\.\d+\.\d+)`)

func writeReplaySection(sb *strings.Builder, tag string, items []string) {
	sb.WriteString("<")
	sb.WriteString(tag)
	sb.WriteString(">\n")
	if len(items) == 0 {
		sb.WriteString("- none recorded\n")
	} else {
		for _, item := range items {
			sb.WriteString("- ")
			sb.WriteString(html.EscapeString(item))
			sb.WriteString("\n")
		}
	}
	sb.WriteString("</")
	sb.WriteString(tag)
	sb.WriteString(">\n")
}

func replayClip(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func replayLooksPending(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "pending") ||
		strings.Contains(s, "remaining") ||
		strings.Contains(s, "todo") ||
		strings.Contains(s, "next step") ||
		strings.Contains(s, "follow-up") ||
		strings.Contains(s, "open question")
}

// spawnedSubagentRE finds sub-agent IDs in spawn_subagent tool results.
// The result string format is fixed ("spawned sub-agent sN (tier=…, …)")
// so we can scrape it reliably.
var spawnedSubagentRE = regexp.MustCompile(`spawned sub-agent (s\d+)`)

// PriorSubagentIDs scans replayed messages for sub-agent IDs that appear
// in spawn_subagent tool results. Returns the sorted list of unique IDs
// and the highest numeric suffix seen — the caller uses the latter to
// bump the manager's spawn counter so fresh workers don't collide with
// IDs the master remembers from before the restart.
func PriorSubagentIDs(messages []llm.Message) (ids []string, maxN int) {
	seen := map[string]bool{}
	for _, m := range messages {
		if m.Role != llm.RoleTool {
			continue
		}
		for _, tr := range m.ToolResults {
			for _, match := range spawnedSubagentRE.FindAllStringSubmatch(tr.Content, -1) {
				id := match[1]
				if seen[id] {
					continue
				}
				seen[id] = true
				ids = append(ids, id)
				if n, err := strconv.Atoi(strings.TrimPrefix(id, "s")); err == nil && n > maxN {
					maxN = n
				}
			}
		}
	}
	sort.Strings(ids)
	return
}

// ResumeReminder builds a system-reminder message warning the master
// that the listed sub-agent IDs are dead and must not be checked /
// sent-to / killed. Returns "" when there's nothing to warn about.
func ResumeReminder(priorIDs []string, nextN int) string {
	if len(priorIDs) == 0 {
		return ""
	}
	return fmt.Sprintf(`<system-reminder>
Session resumed from disk; the previous process has exited. ALL sub-agents named in your prior context (%s) are TERMINATED — do not call check_subagent, send_to_subagent, or kill_subagent on those IDs, and do not refer to them as if they're alive. Their final outputs are already in your tool-result history above; work from those.

If more work is needed, spawn fresh sub-agents. The next ID will be s%d.
</system-reminder>`, strings.Join(priorIDs, ", "), nextN)
}
