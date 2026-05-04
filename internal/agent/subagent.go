package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/tools"
)

type SubagentStatus string

const (
	StatusRunning   SubagentStatus = "running"
	StatusIdle      SubagentStatus = "idle"
	StatusDone      SubagentStatus = "done"
	StatusError     SubagentStatus = "error"
	StatusCancelled SubagentStatus = "cancelled"
)

// SubagentTask is the contract every spawn must specify. Vague spawns are
// rejected upstream (see the spawn_subagent tool schema).
type SubagentTask struct {
	Objective       string   `json:"objective"`
	OutputFormat    string   `json:"output_format"`
	AllowedTools    []string `json:"allowed_tools"`
	TaskBoundaries  string   `json:"task_boundaries"`
	BudgetToolCalls int      `json:"budget_tool_calls"`
	ModelTier       string   `json:"model_tier"` // haiku | sonnet | opus
	Context         string   `json:"context,omitempty"`
}

// Subagent runs a single delegated task in its own goroutine.
type Subagent struct {
	ID      string
	Task    SubagentTask
	Model   string
	Adapter llm.Adapter

	bus      *Bus
	tools    *tools.Registry
	tracker  *llm.Tracker
	maxTurns int

	mu         sync.Mutex
	status     SubagentStatus
	transcript []string
	finalText  string
	cancel     context.CancelFunc
}

func NewSubagent(id string, task SubagentTask, adapter llm.Adapter, model string, registry *tools.Registry, bus *Bus, tracker *llm.Tracker) *Subagent {
	allowed := registry
	if len(task.AllowedTools) > 0 {
		allowed = registry.Subset(task.AllowedTools)
	}
	maxTurns := task.BudgetToolCalls
	if maxTurns <= 0 {
		maxTurns = 10
	}
	return &Subagent{
		ID:       id,
		Task:     task,
		Model:    model,
		Adapter:  adapter,
		bus:      bus,
		tools:    allowed,
		tracker:  tracker,
		maxTurns: maxTurns,
		status:   StatusRunning,
	}
}

func (s *Subagent) Status() SubagentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Subagent) FinalText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalText
}

// Transcript returns a compact list of recent events for check_subagent.
func (s *Subagent) Transcript() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.transcript))
	copy(out, s.transcript)
	return out
}

func (s *Subagent) appendTranscript(line string) {
	s.mu.Lock()
	s.transcript = append(s.transcript, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), line))
	if len(s.transcript) > 200 {
		s.transcript = s.transcript[len(s.transcript)-200:]
	}
	s.mu.Unlock()
}

func (s *Subagent) setStatus(st SubagentStatus) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
}

// Cancel terminates the subagent.
func (s *Subagent) Cancel() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

// Run executes the subagent loop. Should be called in a goroutine.
func (s *Subagent) Run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	system := s.systemPrompt()
	messages := []llm.Message{
		{Role: llm.RoleUser, Text: s.userPrompt()},
	}

	s.bus.Publish(Event{
		Kind:          EvSubagentSpawn,
		SubagentID:    s.ID,
		SubagentTask:  s.Task.Objective,
		SubagentModel: s.Model,
	})

	for turn := 0; turn < s.maxTurns; turn++ {
		req := llm.Request{
			Model:    s.Model,
			System:   system,
			Messages: messages,
			Tools:    s.tools.Definitions(),
		}

		stream, err := s.Adapter.Stream(ctx, req)
		if err != nil {
			s.fail(err)
			return
		}

		var assistantText strings.Builder
		var toolCalls []llm.ToolCall

		for ev := range stream {
			switch ev.Type {
			case llm.StreamEventText:
				assistantText.WriteString(ev.TextDelta)
				s.bus.Publish(Event{Kind: EvSubagentText, SubagentID: s.ID, Text: ev.TextDelta})
			case llm.StreamEventToolCall:
				if ev.ToolCall != nil {
					toolCalls = append(toolCalls, *ev.ToolCall)
					s.appendTranscript(fmt.Sprintf("tool_call: %s", ev.ToolCall.Name))
					s.bus.Publish(Event{Kind: EvSubagentToolCall, SubagentID: s.ID, ToolCall: ev.ToolCall})
				}
			case llm.StreamEventError:
				s.fail(ev.Err)
				return
			case llm.StreamEventDone:
				if ev.Usage != nil {
					s.tracker.Add("subagent:"+s.ID, s.Model, *ev.Usage)
					s.bus.Publish(Event{Kind: EvSubagentUsage, SubagentID: s.ID, Usage: ev.Usage})
				}
			}
		}

		// Build assistant message + tool result messages for next turn.
		assistantMsg := llm.Message{Role: llm.RoleAssistant, Text: assistantText.String(), ToolCalls: toolCalls}
		messages = append(messages, assistantMsg)

		if len(toolCalls) == 0 {
			// Text-only final response.
			s.mu.Lock()
			s.finalText = assistantText.String()
			s.status = StatusDone
			s.mu.Unlock()
			s.appendTranscript("done")
			s.bus.Publish(Event{Kind: EvSubagentDone, SubagentID: s.ID, Text: assistantText.String()})
			return
		}

		// Execute each tool call, one tool-result Message per call.
		for _, tc := range toolCalls {
			result := s.tools.Execute(ctx, tc)
			s.appendTranscript(fmt.Sprintf("tool_done: %s%s", tc.Name, errMark(result.IsError)))
			s.bus.Publish(Event{Kind: EvSubagentToolDone, SubagentID: s.ID, ToolResult: &result})
			messages = append(messages, llm.Message{
				Role:        llm.RoleTool,
				ToolResults: []llm.ToolResult{result},
			})
		}
	}

	// Budget exhausted.
	s.mu.Lock()
	s.status = StatusError
	s.mu.Unlock()
	err := fmt.Errorf("budget_tool_calls (%d) exhausted", s.maxTurns)
	s.appendTranscript("error: " + err.Error())
	s.bus.Publish(Event{Kind: EvSubagentError, SubagentID: s.ID, Err: err})
}

func (s *Subagent) fail(err error) {
	s.mu.Lock()
	s.status = StatusError
	s.mu.Unlock()
	s.appendTranscript("error: " + err.Error())
	s.bus.Publish(Event{Kind: EvSubagentError, SubagentID: s.ID, Err: err})
}

func errMark(b bool) string {
	if b {
		return " [error]"
	}
	return ""
}

func (s *Subagent) systemPrompt() string {
	// XML-tagged for Claude (no-op for OpenAI but harmless).
	return `<role>You are a sub-agent in the ageni harness. You execute one focused task delegated by a master agent and return a structured result.</role>

<rules>
- Stay strictly within the task boundaries you were given.
- Use only the tools listed in <allowed_tools>; do not request others.
- Respect the tool-call budget. If you cannot complete within budget, return what you have plus a clear blocker description.
- Final response: produce exactly one assistant turn that contains a <result>...</result> block matching the requested output_format, followed by a <reasoning>...</reasoning> block summarizing what you did. No tool calls in the final turn.
- Do not invent file paths, function names, or APIs. If you don't know, say so.
</rules>`
}

func (s *Subagent) userPrompt() string {
	var sb strings.Builder
	sb.WriteString("<task>\n")
	sb.WriteString("<objective>" + s.Task.Objective + "</objective>\n")
	sb.WriteString("<output_format>" + s.Task.OutputFormat + "</output_format>\n")
	if s.Task.TaskBoundaries != "" {
		sb.WriteString("<task_boundaries>" + s.Task.TaskBoundaries + "</task_boundaries>\n")
	}
	if len(s.Task.AllowedTools) > 0 {
		sb.WriteString("<allowed_tools>" + strings.Join(s.Task.AllowedTools, ", ") + "</allowed_tools>\n")
	}
	if s.Task.BudgetToolCalls > 0 {
		sb.WriteString(fmt.Sprintf("<budget_tool_calls>%d</budget_tool_calls>\n", s.Task.BudgetToolCalls))
	}
	if s.Task.Context != "" {
		sb.WriteString("<context>" + s.Task.Context + "</context>\n")
	}
	sb.WriteString("</task>\n\nBegin.")
	return sb.String()
}
