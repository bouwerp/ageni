package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/tools"
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
	return msgs, nil
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

// OrientationPrompt generates the auto-sent message that prompts the master
// to orient itself when a session is resumed. It shows the existing todo list
// (if any) and instructs the master to assess what needs to be done.
func OrientationPrompt(todo *tools.TodoWrite) string {
	var sb strings.Builder
	sb.WriteString("<session-orientation>\n")
	sb.WriteString("Session resumed. Review the conversation history above and the current todo list below, then briefly state what you were working on and what to do next.\n\n")

	items := todo.Items()
	pending := 0
	for _, it := range items {
		if it.Status != tools.TodoCompleted {
			pending++
		}
	}

	if len(items) == 0 {
		sb.WriteString("No todo list exists yet. After reviewing the history, use todo_write to create a task list that captures what still needs to be done.")
	} else {
		sb.WriteString("Current todos:\n")
		for _, it := range items {
			mark := "[ ]"
			switch it.Status {
			case tools.TodoInProgress:
				mark = "[~]"
			case tools.TodoCompleted:
				mark = "[x]"
			}
			sb.WriteString(fmt.Sprintf("  %s #%d %s\n", mark, it.ID, it.Content))
		}
		if pending == 0 {
			sb.WriteString("\nAll todos are completed. Determine whether any new work is needed or the session objective is fully met.")
		} else {
			sb.WriteString(fmt.Sprintf("\n%d item(s) pending. Update todo statuses as you make progress.", pending))
		}
	}
	sb.WriteString("\n</session-orientation>")
	return sb.String()
}
