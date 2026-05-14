package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/tools"
)

// activeCorrectionsMax is how many of the most-recent corrections from the
// session log get rendered into the master's active-context tail block.
const activeCorrectionsMax = 8

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

	// leadAdapter / leadModel are an optional separate adapter used for
	// the FIRST iteration of each takeTurns call (planning / integration).
	// When unset, every iteration uses adapter / model. See SetLead.
	leadAdapter llm.Adapter
	leadModel   string

	// criticAdapter / criticModel power the synchronous soundboard tool.
	// When unset, soundboard calls return a "not configured" notice.
	criticAdapter llm.Adapter
	criticModel   string

	// compactAdapter / compactModel are used for context compaction
	// (summarisation). When unset, compaction falls back to the lead
	// adapter (if set) then the primary adapter. A dedicated cheap/fast
	// model is ideal here since the task is pure summarisation.
	compactAdapter llm.Adapter
	compactModel   string

	skillCatalog    string // optional: appended to the cached system prompt
	repoMap         string // optional: rendered repository map appended to the cached prefix
	agentsMD        string // optional: project AGENTS.md instructions (cross-vendor convention)
	correctionsPath string // optional: session corrections.jsonl; tail block reads last K

	// todo gives the master read access to the session todo list so it can
	// include the current state in the active_context block on every turn.
	todo *tools.TodoWrite

	// scrubber, if non-nil, is applied to all assistant text before it is
	// stored in message history or published to the event bus. This prevents
	// secret values that somehow ended up in LLM output from propagating back
	// into the context window or the TUI display.
	scrubber func(string) string

	messages   []llm.Message
	pendingEvs []Event // sub-agent events accumulated since last master turn

	// lastInputTokens tracks the most recent turn's input token count so we
	// can trigger proactive context compaction before hitting the hard limit.
	lastInputTokens int

	// resumed is set by LoadHistory; cleared after the first refreshActiveContext
	// injects a session-resume notice. Ensures the master is reminded of its
	// orchestration role even when there are no active sub-agents on resume.
	resumed bool

	// turnCancel cancels the in-flight LLM call (set by takeTurns; read by
	// CancelCurrent). nil when no call is in flight.
	turnCancel context.CancelFunc

	// masterCaps lists capabilities of the master's own model (e.g. "vision",
	// "reasoning"). Used to inject a runtime capabilities block into the system
	// prompt so the master knows what it can and cannot do natively.
	masterCaps []string

	// subagentCaps lists capabilities available to subagents (union across the
	// pool plus any dedicated providers like VISION_PROVIDER). Injected into the
	// system prompt so the master knows when to delegate capability-gated tasks.
	subagentCaps []string
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

// SetSkillCatalog injects a "<available_skills>...</available_skills>" block
// into the master's stable system prompt. Pass an empty string to clear.
func (m *Master) SetSkillCatalog(catalog string) {
	m.mu.Lock()
	m.skillCatalog = catalog
	m.mu.Unlock()
}

// SetRepoMap injects a "<repo_map>...</repo_map>" block into the master's
// cached system prompt. Pass an empty string to clear.
func (m *Master) SetRepoMap(rendered string) {
	m.mu.Lock()
	m.repoMap = rendered
	m.mu.Unlock()
}

// SetAgentsMD injects the project's AGENTS.md content (cross-vendor
// instruction format used by Codex, Cursor, Amp, Factory, Jules,
// Copilot — see https://agents.md). The rendered string is one or more
// <agents_md scope="..."> blocks pre-formatted by internal/agentsmd.
// Pass an empty string to clear.
func (m *Master) SetAgentsMD(rendered string) {
	m.mu.Lock()
	m.agentsMD = rendered
	m.mu.Unlock()
}

// SetCorrectionsPath tells the master where to read session corrections
// from when refreshing its active-context tail block.
func (m *Master) SetCorrectionsPath(path string) {
	m.mu.Lock()
	m.correctionsPath = path
	m.mu.Unlock()
}

// LoadHistory seeds the master's message buffer with prior turns, used
// when resuming a session via --session <id>. Must be called BEFORE Run.
// The supplied messages are taken as-is; ID consistency for tool-call
// pairs is the caller's responsibility (session.LoadHistory mints
// matching IDs at replay time).
func (m *Master) LoadHistory(messages []llm.Message) {
	m.mu.Lock()
	m.messages = append([]llm.Message(nil), messages...)
	m.resumed = true
	m.mu.Unlock()
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

// PrimaryAdapter returns the master's primary adapter and model. Used by
// SoundboardTool as a self-review fallback when no dedicated critic is configured.
func (m *Master) PrimaryAdapter() (llm.Adapter, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.adapter, m.model
}

// SetLead installs an optional separate adapter for the FIRST iteration
// of each takeTurns call (planning / integration). Subsequent
// iterations within the same takeTurns invocation use the regular
// (worker) adapter set via UpdateAdapter / NewMaster. Pass nil + "" to
// disable lead routing — every iteration then uses the worker adapter.
//
// This is the lead/worker pattern (Goose's GOOSE_LEAD_MODEL): use the
// expensive model for the plan, the cheap one for the tool-execution
// turns that follow. Saves ~50% on tokens at equivalent quality on
// most workloads.
func (m *Master) SetLead(adapter llm.Adapter, model string) {
	m.mu.Lock()
	m.leadAdapter = adapter
	m.leadModel = model
	m.mu.Unlock()
}

// SetCritic installs the adapter used for synchronous soundboard reviews.
// Pass nil + "" to disable. Safe to call from any goroutine.
func (m *Master) SetCritic(adapter llm.Adapter, model string) {
	m.mu.Lock()
	m.criticAdapter = adapter
	m.criticModel = model
	m.mu.Unlock()
}

// SetCompact installs an optional dedicated adapter for context compaction
// (history summarisation). When set, compaction uses this adapter instead
// of falling back to the lead/primary. A cheap, fast model is ideal
// (e.g. gemini-flash, haiku, gpt-4o-mini). Pass nil + "" to disable.
func (m *Master) SetCompact(adapter llm.Adapter, model string) {
	m.mu.Lock()
	m.compactAdapter = adapter
	m.compactModel = model
	m.mu.Unlock()
}

// SetTodo gives the master read access to the session todo list so the
// current task state can be included in the active_context block on every turn.
func (m *Master) SetTodo(t *tools.TodoWrite) {
	m.todo = t
}

// SetScrubber installs a function that is applied to all LLM-generated text
// before it is stored in message history or published to the event bus.
// Use secretStore.Redactor().Scrub to redact known secret values.
func (m *Master) SetScrubber(f func(string) string) {
	m.mu.Lock()
	m.scrubber = f
	m.mu.Unlock()
}

// SetCapabilities records the runtime capabilities of the master's own model
// and those available to subagents. Both are injected into the system prompt
// so the master knows what it can/cannot do natively and when delegation is
// required for capability-gated tasks (e.g. vision, reasoning).
//
// masterCaps: capability strings for the master model ("vision", "reasoning").
// subagentCaps: union of capabilities available across the subagent pool plus
// dedicated providers (e.g. VISION_PROVIDER adds "vision").
func (m *Master) SetCapabilities(masterCaps, subagentCaps []string) {
	m.mu.Lock()
	m.masterCaps = append([]string(nil), masterCaps...)
	m.subagentCaps = append([]string(nil), subagentCaps...)
	m.mu.Unlock()
}

// scrub applies the registered scrubber to s, or returns s unchanged.
func (m *Master) scrub(s string) string {
	m.mu.RLock()
	f := m.scrubber
	m.mu.RUnlock()
	if f == nil || s == "" {
		return s
	}
	return f(s)
}


// Tracker returns the token usage tracker. Used by tools that make their own
// LLM calls (e.g. SoundboardTool) to record usage under the correct role.
func (m *Master) Tracker() *llm.Tracker {
	return m.tracker
}

// CriticAdapter returns the current critic adapter and model. Returns nil, ""
// when soundboard is not configured.
func (m *Master) CriticAdapter() (llm.Adapter, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.criticAdapter, m.criticModel
}


// iteration of takeTurns. Iteration 0 → lead (if set); iteration 1+
// → worker.
func (m *Master) adapterForIter(iter int) (llm.Adapter, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if iter == 0 && m.leadAdapter != nil {
		return m.leadAdapter, m.leadModel
	}
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
//
// Events are coalesced: when several sub-agents complete in a burst (the
// fan-out finish), we drain every immediately-available event from the
// inbox before running takeTurns so they collapse into a single
// integration turn instead of paying N×cached-prefix cost for N near-
// simultaneous completions.
func (m *Master) Run(ctx context.Context, inbox <-chan Event) {
	// On session resume: if the todo list has pending/unfinished items,
	// immediately call takeTurns so the master picks up where it left off
	// without waiting for the user to send a message.
	if m.resumed && m.todo != nil {
		items := m.todo.Items()
		for _, it := range items {
			if it.Status == tools.TodoPending || it.Status == tools.TodoInProgress {
				m.takeTurns(ctx)
				break
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-inbox:
			// EvCancelAll is sent by the TUI when the user presses Esc.
			// Discard accumulated pending sub-agent events and skip the
			// turn so cancelled workers don't re-trigger generation.
			if ev.Kind == EvCancelAll {
				m.pendingEvs = nil
				continue
			}
			runTurn := m.handleInboxEvent(ev)
			// Drain anything else queued right now. This is what coalesces
			// a fan-out burst into one master turn.
		drain:
			for {
				select {
				case ev2 := <-inbox:
					if ev2.Kind == EvCancelAll {
						// Cancel wins: discard pending events and abort turn.
						m.pendingEvs = nil
						runTurn = false
						break drain
					}
					if m.handleInboxEvent(ev2) {
						runTurn = true
					}
				default:
					break drain
				}
			}
			if runTurn {
				m.takeTurns(ctx)
			}
		}
	}
}

// handleInboxEvent applies an inbox event to the master's state. Returns
// true if the event should trigger a master turn (after the caller has
// finished draining the inbox).
func (m *Master) handleInboxEvent(ev Event) bool {
	switch {
	case ev.Kind == EvUserMessage:
		m.messages = append(m.messages, llm.Message{Role: llm.RoleUser, Text: ev.Text})
		return true
	case isSubagentEvent(ev.Kind):
		m.pendingEvs = append(m.pendingEvs, ev)
		return ev.Kind == EvSubagentDone || ev.Kind == EvSubagentError
	}
	return false
}

func isSubagentEvent(k EventKind) bool {
	switch k {
	case EvSubagentSpawn, EvSubagentText, EvSubagentToolCall, EvSubagentToolDone,
		EvSubagentDone, EvSubagentError, EvSubagentUsage:
		return true
	}
	return false
}

// activeContextMarker tags the auto-generated tail block so we can find and
// replace it on the next turn instead of accumulating duplicates in history.
const activeContextMarker = "<active_context_block>"

// refreshActiveContext drops any prior <active_context_block> from the tail
// of the message list and appends a fresh one summarising current state.
// This replaces the old "accumulating sub-agent reminders" pattern: instead
// of letting reminders pile up across turns (each one a permanent token
// cost), we maintain a single self-replacing block that's always current.
//
// pendingEvs are folded into a "since last turn" delta inside the block,
// then cleared.
func (m *Master) refreshActiveContext() {
	// Strip any prior block from the tail.
	for len(m.messages) > 0 {
		last := m.messages[len(m.messages)-1]
		if last.Role == llm.RoleUser && strings.HasPrefix(last.Text, activeContextMarker) {
			m.messages = m.messages[:len(m.messages)-1]
			continue
		}
		break
	}

	subs := m.manager.List()
	corrections := tools.LoadCorrections(m.correctionsPath, activeCorrectionsMax)
	isResumed := m.resumed
	m.resumed = false // clear after first use

	var todoItems []tools.TodoItem
	if m.todo != nil {
		todoItems = m.todo.Items()
	}

	if len(subs) == 0 && len(m.pendingEvs) == 0 && len(corrections) == 0 && !isResumed && len(todoItems) == 0 {
		return
	}

	var sb strings.Builder
	sb.WriteString(activeContextMarker)
	sb.WriteString("\n<active_context>\n")

	if isResumed {
		// All workers from the prior process are terminated. Release any todos
		// that were in_progress so the master can reassign them.
		if m.todo != nil {
			m.todo.ReleaseAllInProgress()
			// Refresh so the todo list below reflects the released state.
			todoItems = m.todo.Items()
		}
		sb.WriteString("SESSION RESUMED. You are the master orchestrator. Your role is unchanged:\n")
		sb.WriteString("- DELEGATE ALL work to sub-agents via spawn_subagent. Never do the work yourself.\n")
		sb.WriteString("- PARALLELISE: fan out independent tasks in the same turn.\n")
		sb.WriteString("- Do NOT call grep/glob/read_file/shell tools directly — spawn workers for those.\n")
		// Prompt master to pick up unfinished work.
		pending := 0
		for _, it := range todoItems {
			if it.Status == tools.TodoPending {
				pending++
			}
		}
		if pending > 0 {
			sb.WriteString(fmt.Sprintf("\n⚠️  The todo list has %d pending item(s) left over from the previous session. Review the list below and immediately continue the unfinished work — do NOT wait for the user to repeat the request.\n", pending))
		}
		sb.WriteString("\n")
	}

	if len(corrections) > 0 {
		sb.WriteString("Corrections in effect (most recent first — honour these over older statements in this conversation):\n")
		for i := len(corrections) - 1; i >= 0; i-- {
			c := corrections[i]
			line := fmt.Sprintf("- WAS: %s | NOW: %s", c.Was, c.Now)
			if c.Why != "" {
				line += " (" + c.Why + ")"
			}
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	if len(todoItems) > 0 {
		sb.WriteString("Current todo list:\n")
		for _, it := range todoItems {
			var status string
			switch it.Status {
			case tools.TodoInProgress:
				status = "▶ IN PROGRESS"
			case tools.TodoCompleted:
				status = "✓ done"
			default:
				status = "○ pending"
			}
			owner := ""
			if it.ClaimedBy != "" {
				owner = " [" + it.ClaimedBy + "]"
			}
			sb.WriteString(fmt.Sprintf("  #%d %s%s: %s\n", it.ID, status, owner, it.Content))
		}
		sb.WriteString("\n")
	}

	if len(subs) > 0 {
		sb.WriteString("Sub-agents (current state):\n")
		for _, s := range subs {
			elapsed := s.Elapsed()
			sb.WriteString(fmt.Sprintf("- %s [%s, running %s] %s\n", s.ID, s.Status(), fmtDuration(elapsed), s.Task.Objective))
		}
	}

	if len(m.pendingEvs) > 0 {
		sb.WriteString("\nNew events since your last turn:\n")
		for _, ev := range m.pendingEvs {
			switch ev.Kind {
			case EvSubagentSpawn:
				sb.WriteString(fmt.Sprintf("- %s spawned (model=%s)\n", ev.SubagentID, ev.SubagentModel))
			case EvSubagentDone:
				sb.WriteString(fmt.Sprintf("- %s finished — call check_subagent(%q) for the final output\n", ev.SubagentID, ev.SubagentID))
				if m.todo != nil {
					m.todo.AutoRelease(ev.SubagentID)
				}
			case EvSubagentError:
				sb.WriteString(fmt.Sprintf("- %s ERROR: %v\n", ev.SubagentID, ev.Err))
				if m.todo != nil {
					m.todo.AutoRelease(ev.SubagentID)
				}
			}
		}
		sb.WriteString("React: inspect via check_subagent, correct via send_to_subagent, kill, or proceed.\n")
		m.pendingEvs = nil
	}

	sb.WriteString("</active_context>")
	m.messages = append(m.messages, llm.Message{Role: llm.RoleUser, Text: sb.String()})
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

	// Regenerate the <active_context> tail block before this turn. The old
	// block (if any) is stripped first so we don't accumulate duplicates.
	m.refreshActiveContext()

	// trimCount caps automatic context-trim retries within one takeTurns call.
	const maxTrims = 3
	var trimCount int

	// idleRetries caps watchdog-triggered retries within one takeTurns call.
	const maxIdleRetries = 2
	var idleRetries int

	// transientRetries caps retries for transient network/protocol errors.
	const maxTransientRetries = 3
	var transientRetries int

	for turn := 0; turn < m.maxTurns; turn++ {
		if ctx.Err() != nil {
			m.bus.Publish(Event{Kind: EvMasterTurnDone, Text: "[cancelled]"})
			return
		}
		adapter, model := m.adapterForIter(turn)
		m.mu.RLock()
		usingLead := turn == 0 && m.leadAdapter != nil
		m.mu.RUnlock()
		trackerRole := "master"
		if usingLead {
			trackerRole = "master/lead"
		}
		req := llm.Request{
			Model:    model,
			System:   m.systemPrompt(),
			Messages: m.messages,
			Tools:    m.tools.Definitions(),
		}
		// Mark the moment the LLM call goes out so the TUI can light up the
		// "thinking" indicator regardless of what triggered the turn (user
		// submit, sub-agent completion, retry).
		m.bus.Publish(Event{Kind: EvMasterTurnStart})
		stream, err := adapter.Stream(ctx, req)
		if err != nil {
			if isContextTooLong(err) && trimCount < maxTrims && m.trimHistory() {
				trimCount++
				m.bus.Publish(Event{Kind: EvFlash, Text: fmt.Sprintf("context window exceeded — trimmed oldest messages (attempt %d/%d)", trimCount, maxTrims)})
				continue
			}
			// If Stream returns an error directly, it means the fallback chain was exhausted
			// or a non-fallbackable error occurred.
			if errors.Is(err, errors.New("fallback chain exhausted")) {
				err = fmt.Errorf("master adapter: fallback chain exhausted trying to talk to models: %w", err)
			} else {
				err = fmt.Errorf("master adapter: primary model failed: %w", err)
			}
			m.bus.Publish(Event{Kind: EvError, Err: err})
			return
		}
		// Wrap with an idle watchdog: if no event arrives within
		// StreamIdleTimeout the wrapper emits a StreamEventError so the
		// loop below doesn't hang silently forever.
		stream = llm.WatchdogStream(stream)

		var assistantText strings.Builder
		var toolCalls []llm.ToolCall
		var cleanCalls []llm.ToolCall
		var reasoningContent string
		var streamErr error

	streamLoop:
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
			case llm.StreamEventThinking:
				reasoningContent += ev.TextDelta
				m.bus.Publish(Event{Kind: EvMasterReasoning, Text: ev.TextDelta})
			case llm.StreamEventToolCall:
				if ev.ToolCall != nil {
					toolCalls = append(toolCalls, *ev.ToolCall)
					m.bus.Publish(Event{Kind: EvMasterToolCall, ToolCall: ev.ToolCall})
				}
			case llm.StreamEventError:
				streamErr = ev.Err
				// Drain the rest of the channel so the sender goroutine can exit.
				go func() {
					for range stream { //nolint:revive
					}
				}()
				break streamLoop
			case llm.StreamEventDone:
				if ev.Usage != nil {
					m.tracker.Add(trackerRole, model, *ev.Usage)
					m.bus.Publish(Event{Kind: EvMasterUsage, Usage: ev.Usage})
					// Track input tokens for proactive compaction.
					m.lastInputTokens = ev.Usage.InputTokens + ev.Usage.CacheReadTokens + ev.Usage.CacheCreationTokens
				}
				// ReasoningContent on Done is the full accumulated string from
				// providers that don't stream it incrementally. Only use it if
				// we haven't already accumulated via StreamEventThinking deltas.
				if reasoningContent == "" && ev.ReasoningContent != "" {
					reasoningContent = ev.ReasoningContent
					m.bus.Publish(Event{Kind: EvMasterReasoning, Text: ev.ReasoningContent})
				}
			}
		}

		if streamErr != nil {
			if isContextTooLong(streamErr) && trimCount < maxTrims && m.trimHistory() {
				trimCount++
				m.bus.Publish(Event{Kind: EvFlash, Text: fmt.Sprintf("context window exceeded — trimmed oldest messages (attempt %d/%d)", trimCount, maxTrims)})
				assistantText.Reset()
				toolCalls = nil
				reasoningContent = ""
				continue
			}
			if llm.IsStreamIdle(streamErr) && idleRetries < maxIdleRetries {
				idleRetries++
				m.bus.Publish(Event{Kind: EvFlash, Text: fmt.Sprintf("model not responding — retrying (%d/%d)…", idleRetries, maxIdleRetries)})
				assistantText.Reset()
				toolCalls = nil
				reasoningContent = ""
				continue
			}
			if llm.IsTransientStreamError(streamErr) && transientRetries < maxTransientRetries {
				transientRetries++
				m.bus.Publish(Event{Kind: EvFlash, Text: fmt.Sprintf("stream interrupted — retrying (%d/%d)…", transientRetries, maxTransientRetries)})
				assistantText.Reset()
				toolCalls = nil
				reasoningContent = ""
				continue
			}
			m.bus.Publish(Event{Kind: EvError, Err: fmt.Errorf("master adapter: streaming error: %w", streamErr)})
			return
		}

		// Only append the assistant message if it has content or tool calls.
		// An empty assistant message (e.g. thinking-only with no response)
		// causes DeepSeek and other providers to reject the next request.
		if assistantText.Len() > 0 || len(toolCalls) > 0 {
			// Scrub any secret values from the assistant's text before it is
			// stored in the message history (and thus re-sent to the LLM on
			// subsequent turns). Tool call arguments are also scrubbed so that
			// a model which hallucinated a credential in its function args
			// doesn't propagate that credential forward.
			cleanText := m.scrub(assistantText.String())
			cleanCalls = toolCalls
			if m.scrubber != nil {
				cleanCalls = make([]llm.ToolCall, len(toolCalls))
				for i, tc := range toolCalls {
					if cleaned := m.scrub(string(tc.Arguments)); cleaned != string(tc.Arguments) {
						tc.Arguments = json.RawMessage(cleaned)
					}
					cleanCalls[i] = tc
				}
			}
			m.messages = append(m.messages, llm.Message{
				Role:             llm.RoleAssistant,
				Text:             cleanText,
				ToolCalls:        cleanCalls,
				ReasoningContent: m.scrub(reasoningContent),
			})
		}

		if len(toolCalls) == 0 {
			m.bus.Publish(Event{Kind: EvMasterTurnDone, Text: assistantText.String()})
			// Check whether proactive compaction should run before the next
			// user-initiated turn. We do it here (after the last tool-free
			// assistant reply) so the compaction itself doesn't stall a
			// multi-step tool loop.
			m.maybeCompactHistory(ctx)
			return
		}

		for _, tc := range cleanCalls {
			result := m.tools.Execute(ctx, tc)
			m.bus.Publish(Event{Kind: EvMasterToolDone, ToolCall: &tc, ToolResult: &result})
			m.messages = append(m.messages, llm.Message{
				Role:        llm.RoleTool,
				ToolResults: []llm.ToolResult{result},
			})
		}
	}
}

// trimHistory drops the oldest complete exchange from m.messages to recover
// from context-window-exceeded errors. It finds the first non-active-context
// RoleUser message after index 0 and removes everything before it, prepending
// a short notice so the model knows the history was truncated. Falls back to
// dropping the first half when no clean turn boundary exists. Returns false
// only if the history is already too short to trim.
func (m *Master) trimHistory() bool {
	msgs := m.messages
	// Find the first RoleUser message after index 0 that is not an
	// active_context block — that marks the start of the second exchange,
	// so everything before it is one complete round-trip we can safely drop.
	for i := 1; i < len(msgs)-1; i++ {
		msg := msgs[i]
		if msg.Role == llm.RoleUser && !strings.HasPrefix(msg.Text, activeContextMarker) {
			notice := llm.Message{
				Role: llm.RoleUser,
				Text: fmt.Sprintf("[Note: %d oldest message(s) removed to fit context window]", i),
			}
			m.messages = append([]llm.Message{notice}, msgs[i:]...)
			return true
		}
	}
	// Fallback: no clean boundary (single long exchange) — drop the first half.
	if len(msgs) > 4 {
		half := len(msgs) / 2
		notice := llm.Message{
			Role: llm.RoleUser,
			Text: fmt.Sprintf("[Note: %d oldest message(s) removed to fit context window]", half),
		}
		m.messages = append([]llm.Message{notice}, msgs[half:]...)
		return true
	}
	return false
}

// compactionThreshold is the input-token count at which proactive context
// compaction is triggered. When a turn consumes more than this many tokens
// the assistant's full conversation history is summarised before the next
// user turn, keeping only the most recent exchanges verbatim.
// 40 000 tokens covers most models' "comfortable" range while leaving
// headroom for the reply and tool calls.
const compactionThreshold = 40_000

// compactionKeepExchanges is the number of complete user/assistant exchanges
// (not counting the active_context tail block) to retain verbatim after
// compaction. Everything older than that is replaced by the summary.
const compactionKeepExchanges = 3

// maybeCompactHistory triggers proactive context compaction when the last
// turn's input token count exceeded compactionThreshold. It is called after
// each terminal (no-tool) assistant turn so the compaction happens "between
// conversations" rather than mid-tool-loop.
func (m *Master) maybeCompactHistory(ctx context.Context) {
	if m.lastInputTokens < compactionThreshold {
		return
	}
	// Only compact when there is enough history to make it worthwhile.
	if len(m.messages) < (compactionKeepExchanges*2)+4 {
		return
	}
	m.bus.Publish(Event{Kind: EvFlash, Text: fmt.Sprintf("context growing (%d tokens) — compacting…", m.lastInputTokens)})
	m.bus.Publish(Event{Kind: EvCompaction, Text: fmt.Sprintf("🗜️ Context compacting… (%d tokens — summarising older messages)", m.lastInputTokens)})
	m.compactHistory(ctx)
}

// compactHistory summarises the older portion of m.messages using the LLM,
// replacing it with a single summary block so that future turns start with a
// much smaller context. The most recent compactionKeepExchanges user/assistant
// exchanges are kept verbatim so the model has immediate conversational context.
func (m *Master) compactHistory(ctx context.Context) {
	msgs := m.messages

	// Strip any active_context tail block — we'll let refreshActiveContext
	// regenerate it before the next real turn.
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role == llm.RoleUser && strings.HasPrefix(last.Text, activeContextMarker) {
			msgs = msgs[:len(msgs)-1]
		} else {
			break
		}
	}

	// Identify the slice boundary: keep the last compactionKeepExchanges
	// complete user/assistant pairs, compact everything before that.
	keepFrom := 0
	exchanges := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			exchanges++
			if exchanges > compactionKeepExchanges {
				keepFrom = i + 1
				break
			}
		}
	}
	if keepFrom == 0 {
		// Not enough history to split; nothing to compact.
		m.messages = msgs
		return
	}

	toSummarise := msgs[:keepFrom]
	toKeep := msgs[keepFrom:]

	// Build a summarisation prompt from the messages to compress.
	var histBuf strings.Builder
	for _, msg := range toSummarise {
		switch msg.Role {
		case llm.RoleUser:
			histBuf.WriteString("USER: " + msg.Text + "\n\n")
		case llm.RoleAssistant:
			histBuf.WriteString("ASSISTANT: " + msg.Text + "\n\n")
		case llm.RoleTool:
			for _, tr := range msg.ToolResults {
				histBuf.WriteString(fmt.Sprintf("TOOL RESULT [%s]: %s\n\n", tr.ToolCallID, tr.Content))
			}
		}
	}

	summariseReq := llm.Request{
		Model: m.model,
		System: `You are a conversation summariser. Your only job is to produce a concise but complete summary of the conversation excerpt you receive. The summary must:
- Preserve all key decisions, constraints, and conclusions reached.
- List all tasks that were completed, pending, or cancelled.
- Note any important file paths, IDs, tool results, or values that were referenced.
- Be written in third-person past tense ("The user asked...", "The assistant planned...").
- Be plain text, no markdown headers, no bullet-point lists — use short prose paragraphs.
- Be as short as possible while retaining all decision-relevant details.`,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "Summarise the following conversation excerpt:\n\n" + histBuf.String()},
		},
	}

	// Use the compact adapter if configured; then lead; then primary.
	// A dedicated cheap/fast model is ideal for summarisation.
	m.mu.RLock()
	adapter := m.adapter
	model := m.model
	if m.compactAdapter != nil {
		adapter = m.compactAdapter
		model = m.compactModel
	} else if m.leadAdapter != nil {
		adapter = m.leadAdapter
		model = m.leadModel
	}
	m.mu.RUnlock()

	summaryCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	stream, err := adapter.Stream(summaryCtx, summariseReq)
	if err != nil {
		m.bus.Publish(Event{Kind: EvFlash, Text: "context compaction failed — keeping history as-is"})
		m.bus.Publish(Event{Kind: EvCompaction, Done: true, Text: "⚠️ Context compaction failed — keeping history as-is"})
		m.messages = msgs
		return
	}

	var summaryBuf strings.Builder
	var compactUsage llm.Usage
	var streamErr error
streamLoop:
	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				break streamLoop
			}
			switch ev.Type {
			case llm.StreamEventText:
				summaryBuf.WriteString(ev.TextDelta)
			case llm.StreamEventDone:
				if ev.Usage != nil {
					compactUsage = *ev.Usage
				}
				break streamLoop
			case llm.StreamEventError:
				streamErr = ev.Err
				break streamLoop
			}
		case <-summaryCtx.Done():
			streamErr = summaryCtx.Err()
			// Drain the channel in the background so the adapter goroutine
			// can exit cleanly without blocking on a send.
			go func() {
				for range stream { //nolint:revive
				}
			}()
			break streamLoop
		}
	}

	if compactUsage.InputTokens+compactUsage.OutputTokens > 0 {
		m.tracker.Add("master/compact", model, compactUsage)
	}

	if streamErr != nil {
		errText := fmt.Sprintf("⚠️ Context compaction error (%v) — keeping history as-is", streamErr)
		m.bus.Publish(Event{Kind: EvFlash, Text: "context compaction error — keeping history as-is"})
		m.bus.Publish(Event{Kind: EvCompaction, Done: true, Text: errText})
		m.messages = msgs
		return
	}

	summary := strings.TrimSpace(summaryBuf.String())
	if summary == "" {
		m.bus.Publish(Event{Kind: EvFlash, Text: "context compaction produced empty summary — keeping history as-is"})
		m.bus.Publish(Event{Kind: EvCompaction, Done: true, Text: "⚠️ Context compaction produced empty summary — keeping history as-is"})
		m.messages = msgs
		return
	}

	summaryMsg := llm.Message{
		Role: llm.RoleUser,
		Text: fmt.Sprintf("[Context compacted — summary of earlier conversation]\n\n%s", summary),
	}

	m.messages = append([]llm.Message{summaryMsg}, toKeep...)
	m.lastInputTokens = 0 // reset so we don't immediately compact again

	result := fmt.Sprintf("context compacted: %d messages → summary + %d recent messages", len(toSummarise), len(toKeep))
	m.bus.Publish(Event{Kind: EvFlash, Text: result})
	m.bus.Publish(Event{Kind: EvCompaction, Done: true,
		Text: fmt.Sprintf("🗜️ Context compacted — summarised %d messages, kept %d recent (–%d messages)", len(toSummarise), len(toKeep), len(toSummarise)),
	})
}


// the model's context window.
func isContextTooLong(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, h := range []string{
		"context_length_exceeded", "context length exceeded",
		"maximum context length", "prompt is too long",
		"too many tokens", "reduce the length",
		"context window", "tokens exceeds",
	} {
		if strings.Contains(msg, h) {
			return true
		}
	}
	return false
}

// fmtDuration renders a duration in a compact, human-readable form.
func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// MarshalCaller is a placeholder so we can extend turn limits per session.
var _ = time.Now

// CanonicalWorkerOutputFormat is the structured response schema the master
// requests from every worker by default. Standardising the shape makes the
// master's integration step deterministic and gives provenance for free —
// the master can scan findings + confidence, decide what to trust, and
// cite path:line references back to the user.
const CanonicalWorkerOutputFormat = `<result>
<summary>One-paragraph plain-English answer covering what you did and what the master needs to know.</summary>
<findings>
- HIGH | path/file.go:LINE | one-line claim about what's there or what was changed
- MED  | path/file.go:LINE | claim with weaker certainty (heuristic / inferred)
- LOW  | path/file.go     | hypothesis, master should verify
</findings>
<unresolved>
- Optional. Open questions or blockers for the master, one per line.
</unresolved>
<touched_paths>
- path/files/you/read.go
- path/files/you/wrote.go
</touched_paths>
</result>
<reasoning>Brief notes on what you searched / tried and why these are the relevant findings.</reasoning>`

func (m *Master) systemPrompt() string {
	m.mu.RLock()
	catalog := m.skillCatalog
	repoMap := m.repoMap
	agentsMD := m.agentsMD
	masterCaps := m.masterCaps
	subagentCaps := m.subagentCaps
	m.mu.RUnlock()

	skillsBlock := ""
	if catalog != "" {
		skillsBlock = "\n\n<available_skills>\n" + catalog + "\n\nWhen a user request matches a skill's trigger phrases or domain, call read_skill(name=\"...\") to load its full instructions before proceeding. Pass topic=\"...\" for sub-references when listed.\n</available_skills>"
	}
	repoMapBlock := ""
	if repoMap != "" {
		repoMapBlock = "\n\n<repo_map>\n" + repoMap + "\n\nUse this map BEFORE calling grep/glob/read_file. It tells you which files exist and what they contain — use it to plan which files to read, then read them with read_file. The map is intentionally compact; if a file you need isn't listed, fall back to glob/grep.\n</repo_map>"
	}
	agentsBlock := ""
	if agentsMD != "" {
		agentsBlock = "\n\n<project_instructions source=\"AGENTS.md\">\n" + agentsMD + "\n\nThese are the project's authoritative agent instructions (cross-vendor convention shared with Codex, Cursor, Amp, Factory, Jules, Copilot). Honour them as if they were given by the user. When multiple <agents_md> scopes apply to a file, the deepest matching scope wins — root-level rules apply unless a nested scope overrides them.\n</project_instructions>"
	}

	capsBlock := buildMasterCapsBlock(masterCaps, subagentCaps)

	// Stable across the session — sits in the cached prefix region.
	return `<role>You are the master agent in the ageni harness — a pure orchestrator. The user talks only to you. You plan, decompose work, and delegate every task to sub-agents. You never do the work yourself. You are not a coder, not a researcher, not an analyst — you are a planning and coordination layer. Workers execute; you only direct.</role>` + skillsBlock + repoMapBlock + agentsBlock + capsBlock + `

<orchestration_rules>
You are the planner and integrator — NEVER the executor. Workers do ALL the legwork. Your tokens are expensive; theirs are cheap. The rules below are absolute constraints, not guidelines.

**ACT SILENTLY. Do NOT write out your plan before calling tools.**
Thinking happens internally. The user does not need — and should not see — a paragraph explaining what you are about to do. Skip the preamble. Skip the breakdown narration. Skip "I'll approach this by...". Call tools directly.

The ONLY text you produce before tool calls is a one-sentence acknowledgement when the request is ambiguous enough to warrant it.

0. **THE MASTER'S PERMITTED TOOLS ARE FINITE.** You may only call:
   spawn_subagent, find_in_codebase, check_subagent, send_to_subagent, kill_subagent, read_skill, soundboard, todo_write
   Calling ANY other tool (grep, glob, read_file, edit_file, shell_exec, open_shell, view_image, …) is a hard violation of the orchestration contract. If you notice yourself about to call one of those, STOP — package the need into a worker's context and spawn.

0b. **MAINTAIN THE TODO LIST. THE LIST IS THE ONLY WORK QUEUE.**
   - **Before spawning ANY worker**, that work must have a corresponding todo item. If you're about to spawn for work that isn't on the list, STOP — add it to the list first with todo_write(action=add), then proceed.
   - On receiving a new request: (1) call todo_write(action=remove) with no IDs to prune all completed items, (2) call todo_write(action=add or replace, items=[...]) to add the new request's items. Include ONLY the items for this request.
   - New items MUST start as "pending". Never add a new item with status "completed".
   - Mark items in_progress (via action=update) when you spawn a worker for them; mark completed when verified.
   - When a phase finishes and more work remains, use action=add to append the new pending items.
   - The todo list is shown in the user's sidebar in real time — it is your primary communication channel about progress.
   - For single-step requests, a single todo item is sufficient; for complex tasks, one item per deliverable.
   - Use the **notes** field (on each item, or via action=update) to store extended context: acceptance criteria, relevant file paths, prior findings, constraints. Keep content short and scannable; put detail in notes. The user can view notes on demand by selecting the item in the sidebar.
   - **Prune proactively**: call todo_write(action=remove) (no IDs) at the start of each new request to drop completed items. Call todo_write(action=remove, ids=[...]) to delete specific items that are no longer relevant (scope changed, cancelled, obsolete).

1. **SOUNDBOARD FOR COMPLEX PLANS.** Call soundboard(plan=...) before spawning workers when:
   - The plan involves **3 or more workers** being spawned.
   - The work is **cross-cutting** (touches many files, multiple systems, or shared interfaces).
   - The operation is **risky or irreversible** (database migrations, auth changes, config changes, deletions).
   - You are **genuinely uncertain** about the right decomposition or approach.

   **Skip soundboard** for simple single-worker tasks, straightforward lookups, or when you are clearly confident about the approach.

   - Give soundboard a concise decomposition: which workers, what each does, how results integrate.
   - After soundboard returns, incorporate any significant concerns before spawning.
   - soundboard may be called in the same turn as spawn_subagent/find_in_codebase, as long as soundboard is called first.

2. **DELEGATE EVERYTHING.** The moment you identify work to be done — any work — spawn a sub-agent (find_in_codebase for searches, spawn_subagent for edits/analysis). There is no task so small that the master should do it directly. "It's just one file" or "it's just a quick look" are not valid reasons to self-execute. If you would need less than one sentence to describe the work to a worker, you are probably about to do it yourself — stop and spawn.

3. **PARALLELISE EVERYTHING INDEPENDENT.** Sub-agents run as concurrent goroutines. Multiple spawn_subagent calls in the SAME turn execute simultaneously. Sequential spawning is correct ONLY when later work depends on earlier work's output. The default for independent tasks is fan-out.

   Examples:
   - "Refactor 3 files" → 3 sub-agents, parallel, one per file (independent)
   - "Find where X, Y, and Z are defined" → 3 find_in_codebase calls, parallel
   - "Audit auth, error handling, and tests" → 3 sub-agents, parallel, one per concern
   - "Implement feature, then test it" → SERIAL: implement first, then test (test depends on impl)
   - "Fix the build, then run benchmarks" → SERIAL: benchmarks depend on a working build

   After fanning out, end your turn. You'll get system-reminder events for each worker's completion. When all are done, integrate their results in your next response.

4. **Anti-patterns — catch yourself doing these and stop:**
   - About to call grep, glob, read_file, or any file/shell tool directly → STOP, spawn a worker
   - About to spawn 3+ workers without calling soundboard first for a complex/risky plan → STOP, call soundboard
   - Just spawned ONE sub-agent and there's clearly more independent work → fan out instead, in the SAME turn
   - Spawning, waiting for done, spawning the next, waiting again → that's serial when it should be parallel
   - Writing a paragraph explaining your decomposition plan → STOP, call tools instead of narrating

5. **Routing by tier (cost-aware):**
   - Trivial lookup (file search, grep, listing) → find_in_codebase OR spawn_subagent model_tier=haiku budget=15
   - Standard task (multi-file edit, ordinary debug, code review) → model_tier=sonnet budget=40
   - Complex/ambiguous → decompose into 3-5 parallel sub-agents budget=60; reserve opus for the final synthesis turn only

6. **Every spawn carries a contract.** Single-sentence objective, precise output_format, task_boundaries, budget. Pre-compute the context (file paths, prior decisions, expected output schema). Don't make a Haiku worker re-discover what you already know. **allowed_tools is optional** — omit it to give the worker full tool access; only restrict it when the task is deliberately read-only (e.g. research/search) or you need to prevent specific side-effects. Never provide a partial list that accidentally omits tools the worker will need (e.g. edit_file, multi_edit).

7. **Use the canonical worker output schema.** Unless the task genuinely needs a different shape, set output_format to:
` + "```\n" + CanonicalWorkerOutputFormat + "\n```" + `
   This gives you findings with path:line citations, a confidence level per finding, an unresolved-questions block, and a list of touched paths — everything you need to integrate parallel workers' results deterministically and cite back to the user.

8. **Curate the worker's context with the structured fields.** Use the spawn_subagent fields:
   - 'repo_facts': "internal/llm/anthropic.go: prompt-caching adapter" lines you already know — saves the worker a discovery round-trip
   - 'prior_findings': attributed selections from earlier workers ("s3 found auth at internal/auth/jwt.go:42") — only what THIS worker needs
   - 'do_not_revisit': paths other parallel workers are handling — stops collisions
   This is how you delegate without losing what you've already learned.
</orchestration_rules>

<monitoring_rules>
- Sub-agents run ASYNCHRONOUSLY in their own goroutines. spawn_subagent returns an ID immediately; the worker is just starting up at that point.
- After fanning out N parallel workers, END YOUR TURN. You will get a system-reminder for each completion event. When all your fan-out workers are done, the next turn integrates their results into one synthesised answer for the user.
- DO NOT call check_subagent, send_to_subagent, or kill_subagent in the same turn as the spawn. Those are for after a worker has reported back.
- **A worker shown as [running, Xs] is doing work — this is NORMAL.** Sub-agents can legitimately run for minutes (searching large codebases, running tests, building code). The elapsed time in the active_context is informational only. Do NOT kill a worker merely because it has been running a long time.
- "No transcript yet" is NOT a reason to kill. A worker that just spawned hasn't run any tools yet; that's normal. Wait for an actual EvSubagentDone, EvSubagentError, or substantive tool-call activity before judging.
- **A worker that returned text not matching the exact output_format schema is NOT stuck — it is DONE.** If the result is close but not perfectly structured, integrate what you can or send a follow-up with send_to_subagent asking it to reformat; do NOT kill and re-spawn unless the result is entirely useless.
- Reasonable triggers for kill_subagent: the worker has clearly gone off-task (wrong files, wrong approach), is stuck in a tool-call loop visible in check_subagent, or has explicitly errored out and re-spawning with sharper instructions is the better path.
- Reasonable triggers for send_to_subagent: the worker is running but you noticed a constraint or correction that will help it finish faster.
- Do not rubber-stamp weak work. If a sub-agent's final <result> doesn't match the requested format or appears wrong, send a correction or kill and re-spawn with sharper instructions.
- You must understand findings before directing follow-up work. Never hand off understanding to another worker.
</monitoring_rules>

<ownership_rules>
You OWN every sub-agent you spawn. The user is not a backstop. The user does not know which workers exist, what their objectives were, or whether they finished. Never ask the user about sub-agent state.

1. **Track your own workers.** You spawned them; you know their IDs and objectives. The active_context block lists every sub-agent's current status on each turn. Read it. Do not ask the user "is s2 done?", "did the worker finish?", or "should I wait for s3?" — those are your problems to solve. If the active_context shows a worker still running, end your turn and wait for its completion event. If it shows done, call check_subagent to read its final output.

2. **Verify before integrating.** Every worker returns a <result>...</result> block. Before treating that result as ground truth:
   - Confirm the result matches the requested output_format (canonical schema by default).
   - Sanity-check HIGH/MED findings against the repo when material — read the cited path:line if a downstream change depends on it.
   - If the result is malformed, off-topic, low-confidence on a load-bearing point, or contradicts another worker's findings, that worker is NOT done. Send a correction with send_to_subagent (if it's still running) or kill + re-spawn with sharper instructions. Do not paper over weak work in your synthesis.

3. **Drive the goal to completion without check-ins.** When the user gives you a goal, your job is to deliver it, not to surface checkpoints for approval. Plan silently, fan out workers, integrate results, verify, and produce the deliverable. If a worker's output reveals follow-up work, do that follow-up immediately. The default cadence is: user-message → (silent) workers → deliverable to user.

   **These are NOT reasons to pause and ask the user:**
   - "Should I proceed?" / "Does this approach look right?" / "Want me to continue?"
   - "I've completed phase 1 — shall I start phase 2?"
   - "I found X. Want me to also fix Y?" when Y is an obvious next step
   - Proposing a plan and waiting for approval before executing it
   - Offering options when one is clearly better or when the user's request implies a direction
   - Any status update that ends with a question instead of action

   When in doubt: act, then summarise what you did. Never: describe, then ask.

4. **Never ask the user to do something you can do yourself.** Before directing any action at the user, ask: "Can a sub-agent do this?" If yes — spawn one.
   - **Never ask the user to:** run a command, edit a file, copy-paste output, trigger a build, open a URL, restart a service, or perform any mechanical action.
   - **Never ask the user to:** re-paste content you could read with a worker, re-run something that failed when you could retry it yourself, or do a step "to be safe" when you could verify it autonomously.
   - If a worker can do it — including read-only inspections, shell commands, test runs, API calls — spawn the worker. Workers have access to the full tool suite.
   - **The only legitimate asks to the user are things that physically require a human or a credential you cannot obtain:** supplying an API key/password, making a decision about irreversible external state (billing, production data, hardware), or resolving a genuine ambiguity with no inferrable answer.

5. **Pause only for genuine blockers.** Stop and ask the user ONLY for things you cannot resolve autonomously:
   - Missing information you genuinely cannot derive: a specific design decision, a credential, a target environment
   - An access/auth wall: API keys, login required, permission denied
   - An irreversible action with material blast radius: force-push, drop table, delete shared infra, send to a real external channel
   - A genuine ambiguity where multiple divergent interpretations exist AND you've already narrowed it to ≤3 concrete options. Frame it as a choice, not an open question.

6. **End your turn cleanly — but always leave a signal.** When workers are still running:
   - Produce a one-sentence status line before ending: "⏳ Waiting for N workers (sX: objective summary…)" so the user can see you're active and what you're waiting on.
   - Do NOT pad with explanations. One sentence max.
   When no workers are running and the goal is met or genuinely blocked, produce one final turn: deliverable + brief integration summary, OR the specific blocker.
</ownership_rules>

<output_discipline>
- **LEAD WITH OUTCOMES, NOT INTENTIONS.** Never write what you are about to do. Write what you have done.
  - ✗ "I'll analyse the codebase and then fix the bug."
  - ✗ "My plan is to: 1) search for X, 2) read Y, 3) update Z."
  - ✓ (call tools silently, then) "Fixed: the null-check in auth.go was missing a guard on the token expiry path."
- When summarizing for the user, be concise. The user wants the result, not the play-by-play.
- File paths and code identifiers should be quoted exactly as found.
- **KEEP INTERNALS INVISIBLE.** The following are implementation details of the ageni harness — never mention them in any user-facing message:
  - Sub-agent IDs (s1, s2, …), worker count, spawn/fan-out decisions
  - Tool names: spawn_subagent, check_subagent, send_to_subagent, kill_subagent, find_in_codebase
  - Output schemas, output_format values, canonical worker format, <result> blocks, <findings> / <touched_paths> / <reasoning> XML tags
  - model_tier, budget, allowed_tools, task_boundaries, or any other spawn parameter
  - The fact that you delegated a task, or that a worker returned a result — integrate the findings silently
  The user sees the side-pane for live orchestration activity. Your text to the user should read as if you did the work personally and are simply reporting what you found.
</output_discipline>

<self_healing>
You MUST be self-healing. When a tool call or provider request returns an error, do not stop — diagnose and recover:

1. **Provider errors (400 / invalid JSON / control character in string):** A previous tool call likely had a malformed argument (e.g. a raw newline inside a string). Identify the offending argument, sanitize it (escape special characters), and retry the call with clean arguments.

2. **Tool errors (✗ result):** Read the error message. Determine if it's a bad argument, a missing file, a permission issue, or a transient failure. Fix the root cause and retry rather than abandoning the goal.

3. **Unknown tool / wrong tool name:** If a tool name was rejected (sanitized or not found), switch to the closest valid alternative or decompose the operation into available tools.

4. **Worker errors or apparent stalls:** NEVER self-execute work because a worker failed or appears stuck. The response to any worker problem is ALWAYS one of:
   - send_to_subagent: if the worker is still running, send a correction or hint
   - kill_subagent + spawn_subagent: if the worker is truly stuck (visible tool-loop in transcript), kill it and re-spawn with sharper instructions, optionally a different model_tier
   - spawn_subagent (second attempt): if the error was transient ("model not responding", "idle", provider 5xx), re-spawn immediately with identical instructions — no instruction change needed
   Doing the work yourself instead of spawning a corrected worker is a HARD CONTRACT VIOLATION regardless of how many workers have failed.

5. **Model-tier retry strategy for stuck workers:** If a haiku worker appears stuck after one re-spawn, retry with model_tier=sonnet. If a sonnet worker appears stuck, retry with model_tier=opus. Only escalate tiers on the SECOND re-spawn of the same task; the first re-spawn should use the same tier.

6. **Retry budget:** Up to 3 spawn attempts per task before escalating to the user with a specific blocker description. Never use all 3 retries on the same unchanged input.

7. **Persistent failure protocol — mandatory after 2 failed attempts on the same problem:**
   When two or more worker attempts at the same goal have failed (different errors, different approaches, same outcome), you are in "stuck" mode. The mandatory recovery sequence is:
   a. **Call soundboard** with a summary of what was tried and what failed. Ask it to suggest an entirely different approach. Do not re-spawn the same plan a third time without a revised strategy.
   b. **Spawn a dedicated research sub-agent** (model_tier=sonnet, budget=40) to investigate the root cause if the failure reason is unknown. Give it the error messages and the files involved. Wait for its findings before spawning a fix worker.
   c. Incorporate soundboard feedback AND research findings into the next spawn. If soundboard and research together cannot surface a viable path, THEN escalate to the user with a precise description of what was attempted and why it failed.
   Spawning a third unchanged attempt without a revised strategy is a HARD CONTRACT VIOLATION.
</self_healing>`
}

// buildMasterCapsBlock produces the <model_capabilities> XML block injected
// into the master's system prompt. It summarises what the master model itself
// can do, and what is available via subagents, so the master knows when and
// how to delegate capability-gated tasks (e.g. vision).
func buildMasterCapsBlock(masterCaps, subagentCaps []string) string {
	if len(masterCaps) == 0 && len(subagentCaps) == 0 {
		return ""
	}

	hasIn := func(caps []string, cap string) bool {
		for _, c := range caps {
			if c == cap {
				return true
			}
		}
		return false
	}

	var sb strings.Builder
	sb.WriteString("\n\n<model_capabilities>")
	sb.WriteString("\nThis block describes what YOUR model can do natively vs what is available via subagents.")

	sb.WriteString("\n\nMaster model capabilities:")
	if len(masterCaps) == 0 {
		sb.WriteString("\n- No special capabilities detected (text/tools only).")
	} else {
		for _, c := range masterCaps {
			switch c {
			case "vision":
				sb.WriteString("\n- vision: your model supports image inputs. However, view_image is ALWAYS a subagent-only tool — you must still delegate image tasks.")
			case "reasoning":
				sb.WriteString("\n- reasoning: your model supports extended chain-of-thought reasoning tokens.")
			default:
				sb.WriteString("\n- " + c)
			}
		}
	}

	sb.WriteString("\n\nSubagent capabilities (what workers can do):")
	if len(subagentCaps) == 0 {
		sb.WriteString("\n- No special capabilities detected in the subagent pool.")
	} else {
		if hasIn(subagentCaps, "vision") {
			sb.WriteString("\n- vision: subagents CAN process images. When a task requires reading or analysing an image, spawn a subagent and give it the image path — it will use view_image.")
		}
		if hasIn(subagentCaps, "reasoning") {
			sb.WriteString("\n- reasoning: subagents CAN use extended reasoning. Prefer model_tier=opus for tasks that benefit from deep deliberation.")
		}
		for _, c := range subagentCaps {
			if c != "vision" && c != "reasoning" {
				sb.WriteString("\n- " + c)
			}
		}
	}

	if !hasIn(masterCaps, "vision") && hasIn(subagentCaps, "vision") {
		sb.WriteString("\n\nIMPORTANT: You do not have native vision. To analyse images, always spawn a subagent with the image path and let it call view_image. Never attempt to process image files yourself.")
	}
	if !hasIn(masterCaps, "vision") && !hasIn(subagentCaps, "vision") {
		sb.WriteString("\n\nNOTE: Neither your model nor the subagent pool have vision capability in this session. Image analysis tasks cannot be performed unless VISION_PROVIDER is configured.")
	}

	sb.WriteString("\n</model_capabilities>")
	return sb.String()
}
