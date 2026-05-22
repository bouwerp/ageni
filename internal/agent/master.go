package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/memory"
	"github.com/bouwerp/ageni/internal/models"
	"github.com/bouwerp/ageni/internal/tools"
)

// activeCorrectionsMax is how many of the most-recent corrections from the
// session log get rendered into the master's ephemeral active-context block.
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

	skillCatalog    string           // optional: appended to the cached system prompt
	roleCatalog     string           // optional: appended to the cached system prompt
	memReg          *memory.Registry // optional: live memory registry injected each turn
	repoMap         string           // optional: rendered repository map appended to the cached prefix
	agentsMD        string           // optional: project AGENTS.md instructions (cross-vendor convention)
	correctionsPath string           // optional: session corrections.jsonl; active context reads last K

	// todo gives the master read access to the session todo list so it can
	// include the current state in the ephemeral active_context on every turn.
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

	// resumed is set by LoadHistory; cleared after the first active-context
	// snapshot injects a session-resume notice. Ensures the master is reminded
	// of its orchestration role even when there are no active sub-agents on
	// resume.
	resumed bool

	// turnCancel cancels the in-flight LLM call (set by takeTurns; read by
	// CancelCurrent). nil when no call is in flight.
	turnCancel context.CancelFunc
	// lastMonitorTurn records the most recent orchestration/monitoring turn,
	// including periodic self-check turns triggered while workers are running.
	lastMonitorTurn time.Time
	paused          bool

	// masterCaps lists capabilities of the master's own model (e.g. "vision",
	// "reasoning"). Used to inject a runtime capabilities block into the system
	// prompt so the master knows what it can and cannot do natively.
	masterCaps []string

	// subagentCaps lists capabilities available to subagents (union across the
	// pool plus any dedicated providers like VISION_PROVIDER). Injected into the
	// system prompt so the master knows when to delegate capability-gated tasks.
	subagentCaps []string

	supervisor *SupervisorState
}

func NewMaster(adapter llm.Adapter, model string, registry *tools.Registry, bus *Bus, tracker *llm.Tracker, manager *Manager) *Master {
	return &Master{
		adapter:    adapter,
		model:      model,
		tools:      registry,
		bus:        bus,
		tracker:    tracker,
		manager:    manager,
		maxTurns:   30,
		supervisor: NewSupervisorState(nil),
	}
}

const (
	monitorTickInterval = 5 * time.Second
	monitorTurnMinGap   = 15 * time.Second
)

// SetSkillCatalog injects a "<available_skills>...</available_skills>" block
// into the master's stable system prompt. Pass an empty string to clear.
func (m *Master) SetSkillCatalog(catalog string) {
	m.mu.Lock()
	m.skillCatalog = catalog
	m.mu.Unlock()
}

// SetRoleCatalog injects a "<available_roles>...</available_roles>" block
// into the master's stable system prompt. Pass an empty string to clear.
func (m *Master) SetRoleCatalog(catalog string) {
	m.mu.Lock()
	m.roleCatalog = catalog
	m.mu.Unlock()
}

func (m *Master) SupervisorState() *SupervisorState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.supervisor
}

// SetMemoryRegistry wires a live memory registry into the master. The block
// returned by memReg.InlineBlock() is injected into every system prompt turn
// so memories are always current without requiring a tool call.
func (m *Master) SetMemoryRegistry(reg *memory.Registry) {
	m.mu.Lock()
	m.memReg = reg
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
// from when building its ephemeral active-context snapshot.
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
// current task state can be included in the active_context on every turn.
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

// adapterForIter chooses between the lead and worker adapters. The lead model
// handles the first iteration of a takeTurns call and later integration turns
// that immediately follow tool results.
func (m *Master) adapterForIter(iter int) (llm.Adapter, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.useLeadAdapterLocked(iter) {
		return m.leadAdapter, m.leadModel
	}
	return m.adapter, m.model
}

func (m *Master) useLeadAdapter(iter int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.useLeadAdapterLocked(iter)
}

func (m *Master) useLeadAdapterLocked(iter int) bool {
	if m.leadAdapter == nil {
		return false
	}
	if iter == 0 {
		return true
	}
	for i := len(m.messages) - 1; i >= 0; i-- {
		switch m.messages[i].Role {
		case llm.RoleTool:
			return true
		case llm.RoleAssistant, llm.RoleUser:
			return false
		}
	}
	return false
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

func (m *Master) Pause() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.paused {
		return false
	}
	m.paused = true
	if m.bus != nil {
		m.bus.Publish(Event{Kind: EvMasterPaused})
	}
	return true
}

func (m *Master) Resume() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.paused {
		return false
	}
	m.paused = false
	if m.bus != nil {
		m.bus.Publish(Event{Kind: EvMasterResumed})
	}
	return true
}

func (m *Master) Paused() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.paused
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
	ticker := time.NewTicker(monitorTickInterval)
	defer ticker.Stop()
	// On session resume: if the todo list has pending/unfinished items,
	// immediately call takeTurns so the master picks up where it left off
	// without waiting for the user to send a message.
	if m.resumed && m.todo != nil && !m.Paused() {
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
		case <-ticker.C:
			if m.handleInboxEvent(Event{Kind: EvTick}) && !m.Paused() {
				m.takeTurns(ctx)
			}
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
			if runTurn && !m.Paused() {
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
	case ev.Kind == EvTick:
		if !m.hasRunningSubagents() {
			return false
		}
		if !m.lastMonitorTurn.IsZero() && time.Since(m.lastMonitorTurn) < monitorTurnMinGap {
			return false
		}
		if m.supervisor == nil {
			return false
		}
		decision, workerID := m.supervisor.Tick()
		if workerID == "" {
			return false
		}
		if decision == SupervisorDecisionNudgeStalledWorker {
			if m.sendStallNudge(workerID) {
				return false
			}
			decision = SupervisorDecisionEscalateStall
		}
		if decision != SupervisorDecisionEscalateStall {
			return false
		}
		recovery := string(SupervisorRecoveryRespawnWorker)
		if snap, ok := m.supervisor.Worker(workerID); ok && snap.RecoveryAction != "" {
			recovery = string(snap.RecoveryAction)
		}
		m.pendingEvs = append(m.pendingEvs, Event{
			Kind:       EvTick,
			SubagentID: workerID,
			Text:       fmt.Sprintf("%s stalled waiting for progress (recovery=%s)", workerID, recovery),
		})
		return true
	case isMonitoringEvent(ev.Kind):
		decision := SupervisorDecisionNone
		if m.supervisor != nil {
			decision = m.supervisor.Observe(ev)
		}
		m.pendingEvs = append(m.pendingEvs, ev)
		return decision == SupervisorDecisionIntegrateResult || decision == SupervisorDecisionEscalateError
	}
	return false
}

func (m *Master) hasRunningSubagents() bool {
	return m.runningSubagentCount() > 0
}

func (m *Master) runningSubagentCount() int {
	count := 0
	for _, s := range m.manager.List() {
		if s.Status() == StatusRunning {
			count++
		}
	}
	return count
}

func isMonitoringEvent(k EventKind) bool {
	switch k {
	case EvSubagentSpawn, EvSubagentTurnStart, EvSubagentText, EvSubagentToolCall, EvSubagentToolDone,
		EvSubagentDone, EvSubagentError, EvSubagentUsage, EvSubagentRetry,
		EvSubagentInbox, EvSubagentPaused, EvSubagentResumed,
		EvShellOpened, EvShellExited, EvShellOutputLoss:
		return true
	}
	return false
}

// buildActiveContext synthesizes a one-turn orchestration snapshot summarising
// current worker/todo/correction state. The returned message is ephemeral: it
// is sent with the next LLM request but never stored in m.messages, so replay
// and compaction don't accumulate meta-context.
func (m *Master) buildActiveContext() *llm.Message {
	subs := m.manager.List()
	corrections := tools.LoadCorrections(m.correctionsPath, activeCorrectionsMax)
	isResumed := m.resumed
	m.resumed = false // clear after first use

	var todoItems []tools.TodoItem
	if m.todo != nil {
		todoItems = m.todo.Items()
	}

	if len(subs) == 0 && len(m.pendingEvs) == 0 && len(corrections) == 0 && !isResumed && len(todoItems) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("<active_context>\n")

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

	if summary := m.buildSupervisionSummary(subs); summary != "" {
		sb.WriteString(summary)
	}

	if len(m.pendingEvs) > 0 {
		if delta := m.buildPendingEventDelta(); delta != "" {
			sb.WriteString(delta)
		}
		m.pendingEvs = nil
	}

	sb.WriteString("</active_context>")
	return &llm.Message{Role: llm.RoleUser, Text: sb.String()}
}

func (m *Master) buildSupervisionSummary(subs []*Subagent) string {
	snaps := m.supervisionSnapshots(subs)
	if len(snaps) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<supervision_summary>\n")
	sb.WriteString(fmt.Sprintf("workers: %d\n", len(snaps)))

	stateOrder := []SupervisorWorkerState{
		SupervisorWorkerRunning,
		SupervisorWorkerThinking,
		SupervisorWorkerWaitingOnTool,
		SupervisorWorkerPaused,
		SupervisorWorkerDoneUnintegrated,
		SupervisorWorkerErrorTerminal,
		SupervisorWorkerStalled,
		SupervisorWorkerCancelled,
	}
	var states []string
	for _, state := range stateOrder {
		count := 0
		for _, snap := range snaps {
			if snap.State == state {
				count++
			}
		}
		if count > 0 {
			states = append(states, fmt.Sprintf("%s=%d", state, count))
		}
	}
	if len(states) > 0 {
		sb.WriteString("states: " + strings.Join(states, ", ") + "\n")
	}
	sb.WriteString("workers:\n")
	for _, snap := range snaps {
		line := fmt.Sprintf("- %s [%s", snap.ID, snap.State)
		if snap.Model != "" {
			line += ", model=" + snap.Model
		}
		line += "]"
		if snap.Objective != "" {
			line += " " + clipText(snap.Objective, 120)
		}
		if snap.RetryCount > 0 {
			line += fmt.Sprintf(" | retries=%d", snap.RetryCount)
		}
		if snap.StallCount > 0 {
			line += fmt.Sprintf(" | stalls=%d", snap.StallCount)
		}
		if snap.ErrorClass != "" && snap.ErrorClass != llm.ErrorClassUnknown {
			line += " | error_class=" + string(snap.ErrorClass)
		}
		if snap.RecoveryAction != "" {
			line += " | recovery=" + string(snap.RecoveryAction)
		}
		if snap.LastError != "" {
			line += " | error=" + clipText(snap.LastError, 120)
		}
		if snap.ResultSnippet != "" && snap.State == SupervisorWorkerDoneUnintegrated {
			line += " | result ready"
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("</supervision_summary>\n\n")
	return sb.String()
}

func (m *Master) supervisionSnapshots(subs []*Subagent) []SupervisorWorkerSnapshot {
	if m.supervisor != nil {
		snaps := m.supervisor.Snapshots()
		if len(snaps) > 0 {
			return snaps
		}
	}

	out := make([]SupervisorWorkerSnapshot, 0, len(subs))
	for _, s := range subs {
		if s == nil {
			continue
		}
		out = append(out, SupervisorWorkerSnapshot{
			ID:        s.ID,
			Objective: s.Task.Objective,
			Model:     s.Model,
			State:     mapSubagentStatusToSupervisorState(s.Status()),
		})
	}
	return out
}

func mapSubagentStatusToSupervisorState(status SubagentStatus) SupervisorWorkerState {
	switch status {
	case StatusPaused:
		return SupervisorWorkerPaused
	case StatusDone:
		return SupervisorWorkerDoneUnintegrated
	case StatusError:
		return SupervisorWorkerErrorTerminal
	case StatusCancelled:
		return SupervisorWorkerCancelled
	default:
		return SupervisorWorkerRunning
	}
}

func (m *Master) sendStallNudge(workerID string) bool {
	sub, ok := m.manager.Get(workerID)
	if !ok || sub == nil {
		return false
	}
	msg := `<system-reminder>
No progress has been observed for a while. Do not finalize yet.
Either make the next concrete tool/model action now, or explicitly summarize the blocker and remaining work in your eventual <result>.
</system-reminder>`
	if !sub.Send(msg) {
		return false
	}
	m.pendingEvs = append(m.pendingEvs, Event{
		Kind:       EvSubagentInbox,
		SubagentID: workerID,
		Text:       "supervisor stall nudge delivered",
	})
	return true
}

type workerEventDelta struct {
	spawned      bool
	model        string
	retryCount   int
	lastRetry    string
	inboxCount   int
	toolName     string
	toolError    string
	paused       bool
	resumed      bool
	doneSnippet  string
	errorText    string
	usageSummary string
	turnStarted  bool
}

func (m *Master) buildPendingEventDelta() string {
	if len(m.pendingEvs) == 0 {
		return ""
	}

	type shellDelta struct {
		openKind   ShellKind
		opened     bool
		exited     bool
		outputLoss int64
	}

	workerIDs := make([]string, 0, len(m.pendingEvs))
	workers := map[string]*workerEventDelta{}
	shellIDs := make([]string, 0, 4)
	shells := map[string]*shellDelta{}
	var ticks []string

	ensureWorker := func(id string) *workerEventDelta {
		if d, ok := workers[id]; ok {
			return d
		}
		d := &workerEventDelta{}
		workers[id] = d
		workerIDs = append(workerIDs, id)
		return d
	}
	ensureShell := func(id string) *shellDelta {
		if d, ok := shells[id]; ok {
			return d
		}
		d := &shellDelta{}
		shells[id] = d
		shellIDs = append(shellIDs, id)
		return d
	}

	for _, ev := range m.pendingEvs {
		switch ev.Kind {
		case EvTick:
			ticks = append(ticks, clipText(ev.Text, 180))
		case EvSubagentSpawn:
			d := ensureWorker(ev.SubagentID)
			d.spawned = true
			d.model = ev.SubagentModel
		case EvSubagentTurnStart:
			ensureWorker(ev.SubagentID).turnStarted = true
		case EvSubagentToolCall:
			if ev.ToolCall != nil {
				ensureWorker(ev.SubagentID).toolName = ev.ToolCall.Name
			}
		case EvSubagentToolDone:
			if ev.ToolResult != nil && ev.ToolResult.IsError {
				ensureWorker(ev.SubagentID).toolError = clipText(ev.ToolResult.Content, 180)
			}
		case EvSubagentRetry:
			d := ensureWorker(ev.SubagentID)
			d.retryCount++
			d.lastRetry = clipText(ev.Text, 180)
		case EvSubagentInbox:
			ensureWorker(ev.SubagentID).inboxCount++
		case EvSubagentUsage:
			if ev.Usage != nil {
				ensureWorker(ev.SubagentID).usageSummary = fmt.Sprintf("usage in=%d out=%d", ev.Usage.InputTokens, ev.Usage.OutputTokens)
			}
		case EvSubagentPaused:
			ensureWorker(ev.SubagentID).paused = true
		case EvSubagentResumed:
			ensureWorker(ev.SubagentID).resumed = true
		case EvSubagentDone:
			if m.todo != nil {
				m.todo.AutoRelease(ev.SubagentID)
			}
			d := ensureWorker(ev.SubagentID)
			if ev.Text != "" {
				snippet := ev.Text
				const maxSnippet = 800
				if len(snippet) > maxSnippet {
					snippet = snippet[:maxSnippet] + "\n… (truncated — call check_subagent for full output)"
				}
				d.doneSnippet = snippet
			}
		case EvSubagentError:
			if m.todo != nil {
				m.todo.AutoRelease(ev.SubagentID)
			}
			if ev.Err != nil {
				ensureWorker(ev.SubagentID).errorText = clipText(ev.Err.Error(), 240)
			} else {
				ensureWorker(ev.SubagentID).errorText = "unknown error"
			}
		case EvShellOpened:
			d := ensureShell(ev.SubagentID)
			d.opened = true
			d.openKind = ev.ShellKind
		case EvShellExited:
			ensureShell(ev.SubagentID).exited = true
		case EvShellOutputLoss:
			ensureShell(ev.SubagentID).outputLoss += ev.Bytes
		}
	}

	var sb strings.Builder
	sb.WriteString("<supervision_delta>\n")
	for _, text := range ticks {
		sb.WriteString("- supervision tick: " + text + "\n")
	}
	for _, id := range workerIDs {
		d := workers[id]
		if d == nil {
			continue
		}
		if d.doneSnippet != "" {
			sb.WriteString(fmt.Sprintf("- %s DONE. Final output:\n<subagent_output id=%q>\n%s\n</subagent_output>\n", id, id, d.doneSnippet))
			continue
		}
		if d.errorText != "" {
			sb.WriteString(fmt.Sprintf("- %s ERROR: %s\n", id, d.errorText))
			continue
		}

		parts := make([]string, 0, 6)
		if d.spawned {
			part := "spawned"
			if d.model != "" {
				part += " (model=" + d.model + ")"
			}
			parts = append(parts, part)
		}
		if d.turnStarted {
			parts = append(parts, "started a new model turn")
		}
		if d.retryCount > 0 {
			part := fmt.Sprintf("retry x%d", d.retryCount)
			if d.lastRetry != "" {
				part += ": " + d.lastRetry
			}
			parts = append(parts, part)
		}
		if d.toolName != "" {
			parts = append(parts, "tool "+d.toolName)
		}
		if d.toolError != "" {
			parts = append(parts, "tool error: "+d.toolError)
		}
		if d.inboxCount > 0 {
			parts = append(parts, fmt.Sprintf("received %d master follow-up(s)", d.inboxCount))
		}
		if d.paused {
			parts = append(parts, "paused")
		}
		if d.resumed {
			parts = append(parts, "resumed")
		}
		if d.usageSummary != "" {
			parts = append(parts, d.usageSummary)
		}
		if len(parts) > 0 {
			sb.WriteString(fmt.Sprintf("- %s %s\n", id, strings.Join(parts, "; ")))
		}
	}
	for _, id := range shellIDs {
		d := shells[id]
		if d == nil {
			continue
		}
		if d.opened {
			sb.WriteString(fmt.Sprintf("- shell %s opened (%s)\n", id, d.openKind))
		}
		if d.exited {
			sb.WriteString(fmt.Sprintf("- shell %s exited\n", id))
		}
		if d.outputLoss > 0 {
			sb.WriteString(fmt.Sprintf("- shell %s output loss: %d byte(s)\n", id, d.outputLoss))
		}
	}
	sb.WriteString("React: process outputs above, correct via send_to_subagent, or proceed.\n")
	sb.WriteString("</supervision_delta>\n")
	return sb.String()
}

func (m *Master) takeTurns(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	turnID := CorrelationIDFromContext(parent)
	if turnID == "" {
		turnID = NewCorrelationID("master")
	}
	ctx = WithCorrelationID(ctx, turnID)
	m.mu.Lock()
	m.turnCancel = cancel
	m.lastMonitorTurn = time.Now()
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.turnCancel = nil
		m.mu.Unlock()
		cancel()
	}()

	// Build a one-turn orchestration snapshot. This context is ephemeral and
	// never becomes part of durable conversation history.
	activeContext := m.buildActiveContext()

	// trimCount caps automatic context-trim retries within one takeTurns call.
	const maxTrims = 3
	var trimCount int

	// idleRetries caps watchdog-triggered retries within one takeTurns call.
	const maxIdleRetries = 2
	var idleRetries int

	// transientRetries caps retries for transient network/protocol errors.
	const maxTransientRetries = 3
	var transientRetries int
	publish := func(e Event) {
		e.CorrelationID = turnID
		m.bus.Publish(e)
	}

	for turn := 0; turn < m.maxTurns; turn++ {
		if ctx.Err() != nil {
			publish(Event{Kind: EvMasterTurnDone, Text: "[cancelled]"})
			return
		}
		adapter, model := m.adapterForIter(turn)
		usingLead := m.useLeadAdapter(turn)
		trackerRole := "master"
		if usingLead {
			trackerRole = "master/lead"
		}
		reqMessages := append([]llm.Message(nil), m.messages...)
		if activeContext != nil {
			reqMessages = append(reqMessages, *activeContext)
		}
		req := llm.Request{
			Model:    model,
			System:   m.systemPrompt(),
			Messages: reqMessages,
			Tools:    m.tools.Definitions(),
		}
		// Mark the moment the LLM call goes out so the TUI can light up the
		// "thinking" indicator regardless of what triggered the turn (user
		// submit, sub-agent completion, retry).
		publish(Event{Kind: EvMasterTurnStart})
		stream, err := adapter.Stream(ctx, req)
		if err != nil {
			if isContextTooLong(err) && trimCount < maxTrims && m.trimHistory() {
				trimCount++
				publish(Event{Kind: EvFlash, Text: fmt.Sprintf("context window exceeded — trimmed oldest messages (attempt %d/%d)", trimCount, maxTrims)})
				continue
			}
			// If Stream returns an error directly, it means the fallback chain was exhausted
			// or a non-fallbackable error occurred.
			if errors.Is(err, llm.ErrFallbackChainExhausted) {
				err = fmt.Errorf("master adapter: fallback chain exhausted trying to talk to models: %w", err)
			} else {
				err = fmt.Errorf("master adapter: primary model failed: %w", err)
			}
			publish(Event{Kind: EvError, Err: err})
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
				publish(Event{Kind: EvMasterText, Text: ev.TextDelta})
			case llm.StreamEventThinking:
				reasoningContent += ev.TextDelta
				publish(Event{Kind: EvMasterReasoning, Text: ev.TextDelta})
			case llm.StreamEventToolCall:
				if ev.ToolCall != nil {
					toolCalls = append(toolCalls, *ev.ToolCall)
					publish(Event{Kind: EvMasterToolCall, ToolCall: ev.ToolCall})
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
					publish(Event{Kind: EvMasterUsage, Usage: ev.Usage})
					// Track input tokens for proactive compaction.
					m.lastInputTokens = ev.Usage.InputTokens + ev.Usage.CacheReadTokens + ev.Usage.CacheCreationTokens
				}
				// ReasoningContent on Done is the full accumulated string from
				// providers that don't stream it incrementally. Only use it if
				// we haven't already accumulated via StreamEventThinking deltas.
				if reasoningContent == "" && ev.ReasoningContent != "" {
					reasoningContent = ev.ReasoningContent
					publish(Event{Kind: EvMasterReasoning, Text: ev.ReasoningContent})
				}
			}
		}

		if streamErr != nil {
			if isContextTooLong(streamErr) && trimCount < maxTrims && m.trimHistory() {
				trimCount++
				publish(Event{Kind: EvFlash, Text: fmt.Sprintf("context window exceeded — trimmed oldest messages (attempt %d/%d)", trimCount, maxTrims)})
				assistantText.Reset()
				toolCalls = nil
				reasoningContent = ""
				continue
			}
			if llm.IsStreamIdle(streamErr) && idleRetries < maxIdleRetries {
				idleRetries++
				publish(Event{Kind: EvFlash, Text: fmt.Sprintf("model not responding — retrying (%d/%d)…", idleRetries, maxIdleRetries)})
				assistantText.Reset()
				toolCalls = nil
				reasoningContent = ""
				continue
			}
			if llm.IsTransientStreamError(streamErr) && transientRetries < maxTransientRetries {
				transientRetries++
				publish(Event{Kind: EvFlash, Text: fmt.Sprintf("stream interrupted — retrying (%d/%d)…", transientRetries, maxTransientRetries)})
				assistantText.Reset()
				toolCalls = nil
				reasoningContent = ""
				continue
			}
			publish(Event{Kind: EvError, Err: fmt.Errorf("master adapter: streaming error: %w", streamErr)})
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
			publish(Event{Kind: EvMasterTurnDone, Text: assistantText.String()})
			// Check whether proactive compaction should run before the next
			// user-initiated turn. We do it here (after the last tool-free
			// assistant reply) so the compaction itself doesn't stall a
			// multi-step tool loop.
			m.maybeCompactHistory(ctx)
			return
		}

		for _, tc := range cleanCalls {
			if ctx.Err() != nil {
				break
			}
			result := m.tools.Execute(ctx, tc)
			publish(Event{Kind: EvMasterToolDone, ToolCall: &tc, ToolResult: &result})
			m.messages = append(m.messages, llm.Message{
				Role:        llm.RoleTool,
				ToolResults: []llm.ToolResult{result},
			})
		}
	}
}

// trimHistory drops the oldest complete exchange from m.messages to recover
// from context-window-exceeded errors. It finds the first RoleUser message
// after index 0 and removes everything before it, prepending a short notice so
// the model knows the history was truncated. Falls back to dropping the first
// half when no clean turn boundary exists. Returns false only if the history is
// already too short to trim.
func (m *Master) trimHistory() bool {
	msgs := m.messages
	// Find the first RoleUser message after index 0 — that marks the start of
	// the second exchange, so everything before it is one complete round-trip
	// we can safely drop.
	for i := 1; i < len(msgs)-1; i++ {
		msg := msgs[i]
		if msg.Role == llm.RoleUser {
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

// compactionKeepExchanges is the number of complete user/assistant exchanges to
// retain verbatim after compaction. Everything older than that is replaced by
// the summary.
const compactionKeepExchanges = 3

const compactedContextTag = "compacted_context"

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

func isCompactedContextBlock(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "<"+compactedContextTag) && strings.Contains(trimmed, "</"+compactedContextTag+">")
}

func normalizeCompactedContext(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	if isCompactedContextBlock(trimmed) {
		return trimmed, true
	}
	return fmt.Sprintf(`<%s>
<summary>%s</summary>
<decisions>
- none recorded
</decisions>
<completed>
- none recorded
</completed>
<pending>
- none recorded
</pending>
<artifacts>
- none recorded
</artifacts>
</%s>`, compactedContextTag, html.EscapeString(trimmed), compactedContextTag), false
}

// compactHistory summarises the older portion of m.messages using the LLM,
// replacing it with a single structured context block so that future turns
// start with a much smaller but still machine-readable history. The most recent
// compactionKeepExchanges user/assistant exchanges are kept verbatim so the
// model has immediate conversational context.
func (m *Master) compactHistory(ctx context.Context) {
	msgs := m.messages

	// Identify the slice boundary: keep the last compactionKeepExchanges
	// complete user/assistant pairs, compact everything before that.
	keepFrom := 0
	exchanges := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			exchanges++
			if exchanges > compactionKeepExchanges {
				keepFrom = len(msgs)
				for j := i + 1; j < len(msgs); j++ {
					if msgs[j].Role == llm.RoleUser {
						keepFrom = j
						break
					}
				}
				break
			}
		}
	}
	if keepFrom <= 0 || keepFrom >= len(msgs) {
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
		System: `You are a conversation compactor. Produce ONLY XML with this exact root schema:
<compacted_context>
<summary>One concise paragraph covering the overall goal, important decisions, and current state.</summary>
<decisions>
- key decision or constraint
</decisions>
<completed>
- work or tasks that were finished
</completed>
<pending>
- work, risks, or tasks that remain open
</pending>
<artifacts>
- important file paths, IDs, tool outputs, models, or values worth preserving
</artifacts>
</compacted_context>

Rules:
- Keep each list short and decision-relevant.
- Preserve concrete file paths, IDs, and values when they matter.
- Use "- none recorded" when a section would otherwise be empty.
- No prose outside the XML block.
- Do not emit markdown fences.`,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "Compact the following conversation excerpt into the required XML schema:\n\n" + histBuf.String()},
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

	summary, structured := normalizeCompactedContext(summaryBuf.String())
	if summary == "" {
		m.bus.Publish(Event{Kind: EvFlash, Text: "context compaction produced empty summary — keeping history as-is"})
		m.bus.Publish(Event{Kind: EvCompaction, Done: true, Text: "⚠️ Context compaction produced empty summary — keeping history as-is"})
		m.messages = msgs
		return
	}
	if !structured {
		m.bus.Publish(Event{Kind: EvFlash, Text: "context compaction returned unstructured output — normalizing"})
	}

	summaryMsg := llm.Message{Role: llm.RoleUser, Text: summary}

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
	return llm.IsContextLimitError(err)
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

func clipText(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
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

const systemPromptBudgetTokens = 8_000

type promptContextBlock struct {
	name string
	text string
}

func fitPromptBlocks(budget int, blocks []promptContextBlock) string {
	if budget <= 0 {
		return ""
	}
	if budget > 8 {
		budget -= 8 // leave a little headroom for estimator drift
	}
	noticeReserve := models.EstimateTokens("\n\n<prompt_budget_notice>\nAuxiliary context omitted to stay within the system prompt budget. Fetch anything missing on demand instead of assuming it was included.\n</prompt_budget_notice>")
	workingBudget := budget
	if noticeReserve < workingBudget {
		workingBudget -= noticeReserve
	}
	var sb strings.Builder
	var omitted []string
	var truncated []string
	for _, block := range blocks {
		text := strings.TrimSpace(block.text)
		if text == "" {
			continue
		}
		rendered := "\n\n" + text
		if est := models.EstimateTokens(rendered); est <= workingBudget {
			sb.WriteString(rendered)
			workingBudget -= est
			continue
		}
		if partial := truncatePromptContextBlock(text, workingBudget); partial != "" {
			sb.WriteString(partial)
			workingBudget -= models.EstimateTokens(partial)
			truncated = append(truncated, block.name)
			continue
		}
		omitted = append(omitted, block.name)
	}
	if len(omitted) == 0 && len(truncated) == 0 {
		return sb.String()
	}
	var status []string
	if len(truncated) > 0 {
		status = append(status, "truncated to fit the system prompt budget: "+strings.Join(truncated, ", "))
	}
	if len(omitted) > 0 {
		status = append(status, "omitted to stay within the system prompt budget: "+strings.Join(omitted, ", "))
	}
	notice := "\n\n<prompt_budget_notice>\nAuxiliary context " +
		strings.Join(status, ". Also ") +
		". Fetch anything missing on demand instead of assuming it was included.\n</prompt_budget_notice>"
	if models.EstimateTokens(notice) <= budget-workingBudget {
		sb.WriteString(notice)
	}
	return sb.String()
}

func truncatePromptContextBlock(text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	const marker = "... (truncated to fit prompt budget)"
	openEnd := strings.Index(text, ">")
	closeStart := strings.LastIndex(text, "</")
	if openEnd < 0 || closeStart <= openEnd {
		return ""
	}
	prefix := text[:openEnd+1]
	suffix := text[closeStart:]
	body := strings.TrimSpace(text[openEnd+1 : closeStart])
	render := func(content string) string {
		if content == "" {
			return "\n\n" + prefix + "\n" + marker + "\n" + suffix
		}
		return "\n\n" + prefix + "\n" + strings.TrimSpace(content) + "\n" + marker + "\n" + suffix
	}
	minimum := render("")
	if models.EstimateTokens(minimum) > budget {
		return ""
	}
	best := minimum
	low, high := 0, len(body)
	for low <= high {
		mid := (low + high) / 2
		candidate := render(body[:mid])
		if models.EstimateTokens(candidate) <= budget {
			best = candidate
			low = mid + 1
			continue
		}
		high = mid - 1
	}
	return best
}

func (m *Master) systemPrompt() string {
	m.mu.RLock()
	catalog := m.skillCatalog
	roleCat := m.roleCatalog
	memReg := m.memReg
	messages := append([]llm.Message(nil), m.messages...)
	repoMap := m.repoMap
	agentsMD := m.agentsMD
	masterCaps := m.masterCaps
	subagentCaps := m.subagentCaps
	m.mu.RUnlock()

	roleBlock := `<role>You are the master agent in the ageni harness — a pure orchestrator. The user talks only to you. You plan, decompose work, and delegate every task to sub-agents. You never do the work yourself. You are not a coder, not a researcher, not an analyst — you are a planning and coordination layer. Workers execute; you only direct.</role>`
	skillsBlock := ""
	if catalog != "" {
		skillsBlock = "\n\n<available_skills>\n" + catalog + "\n\nWhen a user request matches a skill's trigger phrases or domain, call read_skill(name=\"...\") to load its full instructions before proceeding. Pass topic=\"...\" for sub-references when listed.\n</available_skills>"
	}
	rolesBlock := ""
	if roleCat != "" {
		rolesBlock = "\n\n<available_roles>\n" + roleCat + "\n\nWhen spawning a sub-agent, set role=\"<name>\" to apply a predefined persona. The role sets model_tier, budget, skill, persona instructions, and task_boundaries automatically. Explicit spawn params override role defaults.\n</available_roles>"
	}
	memoriesBlock := ""
	if memReg != nil {
		if block := memReg.InlineBlockForQuery(latestMemoryQuery(messages), 6); block != "" {
			memoriesBlock = "\n\n" + block
		}
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
	optionalBlocks := []promptContextBlock{
		{name: "AGENTS.md project instructions", text: agentsBlock},
		{name: "persistent memories", text: memoriesBlock},
		{name: "available skills catalog", text: skillsBlock},
		{name: "available roles catalog", text: rolesBlock},
		{name: "repository map", text: repoMapBlock},
	}

	coreBlock := `

<orchestration_rules>
You are the planner and integrator — NEVER the executor. Workers do ALL the legwork. Your tokens are expensive; theirs are cheap. The rules below are absolute constraints, not guidelines.

**ACT SILENTLY. Do NOT write out your plan before calling tools.**
Thinking happens internally. The user does not need — and should not see — a paragraph explaining what you are about to do. Skip the preamble. Skip the breakdown narration. Skip "I'll approach this by...". Call tools directly.

The ONLY text you produce before tool calls is a one-sentence acknowledgement when the request is ambiguous enough to warrant it.

0. **THE MASTER'S PERMITTED TOOLS ARE FINITE.** You may only call:
   spawn_subagent, find_in_codebase, check_subagent, send_to_subagent, kill_subagent, read_skill, soundboard, todo_write, remember, recall, forget
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
   - Trivial lookup (file search, grep, listing) → find_in_codebase OR spawn_subagent model_tier=haiku budget=50
   - Standard task (multi-file edit, ordinary debug, code review) → model_tier=sonnet budget=150
   - Complex/ambiguous → decompose into 3-5 parallel sub-agents budget=200; reserve opus for the final synthesis turn only

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
   b. **Spawn a dedicated research sub-agent** (model_tier=sonnet, budget=100) to investigate the root cause if the failure reason is unknown. Give it the error messages and the files involved. Wait for its findings before spawning a fix worker.
   c. Incorporate soundboard feedback AND research findings into the next spawn. If soundboard and research together cannot surface a viable path, THEN escalate to the user with a precise description of what was attempted and why it failed.
   Spawning a third unchanged attempt without a revised strategy is a HARD CONTRACT VIOLATION.
</self_healing>`

	optionalBudget := systemPromptBudgetTokens - models.EstimateTokens(roleBlock) - models.EstimateTokens(capsBlock) - models.EstimateTokens(coreBlock)
	optionalBudget -= 32 // small safety margin for prompt-estimation drift
	if optionalBudget < 0 {
		optionalBudget = 0
	}

	// Stable across the session — sits in the cached prefix region. Auxiliary
	// context is capped so dynamic catalogs and repo metadata can't silently
	// crowd out the orchestration contract.
	return roleBlock + fitPromptBlocks(optionalBudget, optionalBlocks) + capsBlock + coreBlock
}

func latestMemoryQuery(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != llm.RoleUser {
			continue
		}
		text := strings.TrimSpace(msg.Text)
		if text == "" ||
			strings.HasPrefix(text, "<active_context>") ||
			strings.HasPrefix(text, "<system-reminder>") ||
			strings.HasPrefix(text, "<session-resume>") ||
			strings.HasPrefix(text, "<compacted_context") {
			continue
		}
		return text
	}
	return ""
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
