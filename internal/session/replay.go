package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
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
	return msgs, nil
}
