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

// Master is the user-facing agent. It runs a single loop that:
//   - waits for a user message
//   - takes one or more turns until the model produces text without tool calls
//   - between turns, drains pending sub-agent events into a system-reminder
//     so the master can react ("monitor and correct")
type Master struct {
	mu      sync.RWMutex
	adapter llm.Adapter
	model   string

	tools    *tools.Registry
	bus      *Bus
	tracker  *llm.Tracker
	manager  *Manager
	maxTurns int

	messages   []llm.Message
	pendingEvs []Event // sub-agent events accumulated since last master turn

	// turnCancel cancels the in-flight LLM call (set by takeTurns; read by
	// CancelCurrent). nil when no call is in flight.
	turnCancel context.CancelFunc
}

func NewMaster(adapter llm.Adapter, model string, registry *tools.Registry, bus *Bus, tracker *llm.Tracker, manager *Manager) *Master {
	return &Master{
		adapter:  adapter,
		model:    model,
		tools:    registry,
		bus:      bus,
		tracker:  tracker,
		manager:  manager,
		maxTurns: 30,
	}
}

// UpdateAdapter swaps the live adapter+model. Safe to call from any
// goroutine; takes effect on the next LLM call (in-flight calls finish on
// the old adapter).
func (m *Master) UpdateAdapter(adapter llm.Adapter, model string) {
	m.mu.Lock()
	m.adapter = adapter
	m.model = model
	m.mu.Unlock()
}

func (m *Master) currentAdapter() (llm.Adapter, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.adapter, m.model
}

// CancelCurrent interrupts any in-flight LLM call. The master loop stays
// alive and processes the next event in its inbox.
func (m *Master) CancelCurrent() {
	m.mu.Lock()
	c := m.turnCancel
	m.mu.Unlock()
	if c != nil {
		c()
	}
}

// Run drives the master loop. inbox carries events the master must act on
// (user messages, sub-agent updates the master should see). Returns when
// ctx is cancelled.
func (m *Master) Run(ctx context.Context, inbox <-chan Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-inbox:
			if ev.Kind == EvUserMessage {
				m.messages = append(m.messages, llm.Message{Role: llm.RoleUser, Text: ev.Text})
				m.takeTurns(ctx)
			} else if isSubagentEvent(ev.Kind) {
				m.pendingEvs = append(m.pendingEvs, ev)
				// If a sub-agent finished or errored, wake the master to react.
				if ev.Kind == EvSubagentDone || ev.Kind == EvSubagentError {
					m.injectSubagentReminder()
					m.takeTurns(ctx)
				}
			}
		}
	}
}

func isSubagentEvent(k EventKind) bool {
	switch k {
	case EvSubagentSpawn, EvSubagentText, EvSubagentToolCall, EvSubagentToolDone,
		EvSubagentDone, EvSubagentError, EvSubagentUsage:
		return true
	}
	return false
}

// injectSubagentReminder folds pending sub-agent events into a single
// user-role <system-reminder> so the master can react on its next turn.
// The reminder is volatile (per-turn) so it doesn't pollute the cached prefix.
func (m *Master) injectSubagentReminder() {
	if len(m.pendingEvs) == 0 {
		return
	}
	var sb strings.Builder
	sb.WriteString("<system-reminder>\nSub-agent activity since your last turn:\n")
	for _, ev := range m.pendingEvs {
		switch ev.Kind {
		case EvSubagentSpawn:
			sb.WriteString(fmt.Sprintf("- %s spawned (model=%s, task=%q)\n", ev.SubagentID, ev.SubagentModel, ev.SubagentTask))
		case EvSubagentToolCall:
			if ev.ToolCall != nil {
				sb.WriteString(fmt.Sprintf("- %s called tool %s\n", ev.SubagentID, ev.ToolCall.Name))
			}
		case EvSubagentToolDone:
			if ev.ToolResult != nil {
				mark := ""
				if ev.ToolResult.IsError {
					mark = " [ERROR]"
				}
				sb.WriteString(fmt.Sprintf("- %s tool finished%s\n", ev.SubagentID, mark))
			}
		case EvSubagentDone:
			sb.WriteString(fmt.Sprintf("- %s finished (use check_subagent to read final output)\n", ev.SubagentID))
		case EvSubagentError:
			sb.WriteString(fmt.Sprintf("- %s ERROR: %v\n", ev.SubagentID, ev.Err))
		}
	}
	sb.WriteString("Decide whether to inspect (check_subagent), correct (send_to_subagent), kill, or proceed.\n</system-reminder>")
	m.messages = append(m.messages, llm.Message{Role: llm.RoleUser, Text: sb.String()})
	m.pendingEvs = nil
}

func (m *Master) takeTurns(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	m.turnCancel = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.turnCancel = nil
		m.mu.Unlock()
		cancel()
	}()

	for turn := 0; turn < m.maxTurns; turn++ {
		if ctx.Err() != nil {
			m.bus.Publish(Event{Kind: EvMasterTurnDone, Text: "[cancelled]"})
			return
		}
		adapter, model := m.currentAdapter()
		req := llm.Request{
			Model:    model,
			System:   m.systemPrompt(),
			Messages: m.messages,
			Tools:    m.tools.Definitions(),
		}
		stream, err := adapter.Stream(ctx, req)
		if err != nil {
			m.bus.Publish(Event{Kind: EvError, Err: err})
			return
		}

		var assistantText strings.Builder
		var toolCalls []llm.ToolCall

		for ev := range stream {
			select {
			case <-ctx.Done():
				return
			default:
			}
			switch ev.Type {
			case llm.StreamEventText:
				assistantText.WriteString(ev.TextDelta)
				m.bus.Publish(Event{Kind: EvMasterText, Text: ev.TextDelta})
			case llm.StreamEventToolCall:
				if ev.ToolCall != nil {
					toolCalls = append(toolCalls, *ev.ToolCall)
					m.bus.Publish(Event{Kind: EvMasterToolCall, ToolCall: ev.ToolCall})
				}
			case llm.StreamEventError:
				m.bus.Publish(Event{Kind: EvError, Err: ev.Err})
				return
			case llm.StreamEventDone:
				if ev.Usage != nil {
					m.tracker.Add("master", model, *ev.Usage)
					m.bus.Publish(Event{Kind: EvMasterUsage, Usage: ev.Usage})
				}
			}
		}

		m.messages = append(m.messages, llm.Message{
			Role:      llm.RoleAssistant,
			Text:      assistantText.String(),
			ToolCalls: toolCalls,
		})

		if len(toolCalls) == 0 {
			m.bus.Publish(Event{Kind: EvMasterTurnDone, Text: assistantText.String()})
			return
		}

		for _, tc := range toolCalls {
			result := m.tools.Execute(ctx, tc)
			m.bus.Publish(Event{Kind: EvMasterToolDone, ToolCall: &tc, ToolResult: &result})
			m.messages = append(m.messages, llm.Message{
				Role:        llm.RoleTool,
				ToolResults: []llm.ToolResult{result},
			})
		}
	}
}

// MarshalCaller is a placeholder so we can extend turn limits per session.
var _ = time.Now

func (m *Master) systemPrompt() string {
	// Stable across the session — sits in the cached prefix region.
	return `<role>You are the master agent in the ageni harness. The user talks only to you. You decompose work, delegate to sub-agents, monitor their progress, and synthesize the result.</role>

<orchestration_rules>
- You direct, sub-agents execute. Default to delegating; do work yourself only for trivial single-step tasks or final synthesis.
- Routing by tier (cost-aware):
  - Trivial lookup (file search, grep, listing) → spawn_subagent with model_tier="haiku", budget_tool_calls<=5.
  - Standard task (multi-file edit, ordinary debug, code review) → model_tier="sonnet", budget<=15.
  - Complex/ambiguous → decompose into 3-5 PARALLEL sub-agents; reserve opus for final synthesis only.
- Every spawn must include a single-sentence objective AND a precise output_format. Vague spawns cause duplicated work — refuse to dispatch otherwise.
- Pre-compute context for sub-agents: name files explicitly, supply expected output schema, set hard constraints. Don't make a Haiku worker re-discover what you already know.
- Spawn parallel sub-agents in a SINGLE turn when work is independent.
</orchestration_rules>

<monitoring_rules>
- After spawning, you receive sub-agent events via system-reminders. React: inspect via check_subagent, correct via send_to_subagent, kill if hopeless, or proceed.
- Do not rubber-stamp weak work. If a sub-agent's output doesn't match the requested format or appears wrong, send a correction or kill and re-spawn with sharper instructions.
- You must understand findings before directing follow-up work. Never hand off understanding to another worker.
</monitoring_rules>

<output_discipline>
- When summarizing for the user, be concise. The user wants the result, not the play-by-play.
- File paths and code identifiers should be quoted exactly as found.
</output_discipline>`
}
