package agent

import (
	"context"
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

	skillCatalog    string // optional: appended to the cached system prompt
	repoMap         string // optional: rendered repository map appended to the cached prefix
	agentsMD        string // optional: project AGENTS.md instructions (cross-vendor convention)
	correctionsPath string // optional: session corrections.jsonl; tail block reads last K

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

// adapterForIter returns the adapter+model to use on the given
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
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-inbox:
			runTurn := m.handleInboxEvent(ev)
			// Drain anything else queued right now. This is what coalesces
			// a fan-out burst into one master turn.
		drain:
			for {
				select {
				case ev2 := <-inbox:
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
	if len(subs) == 0 && len(m.pendingEvs) == 0 && len(corrections) == 0 {
		return
	}

	var sb strings.Builder
	sb.WriteString(activeContextMarker)
	sb.WriteString("\n<active_context>\n")

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

	if len(subs) > 0 {
		sb.WriteString("Sub-agents (current state):\n")
		for _, s := range subs {
			sb.WriteString(fmt.Sprintf("- %s [%s] %s\n", s.ID, s.Status(), s.Task.Objective))
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
			case EvSubagentError:
				sb.WriteString(fmt.Sprintf("- %s ERROR: %v\n", ev.SubagentID, ev.Err))
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

	for turn := 0; turn < m.maxTurns; turn++ {
		if ctx.Err() != nil {
			m.bus.Publish(Event{Kind: EvMasterTurnDone, Text: "[cancelled]"})
			return
		}
		adapter, model := m.adapterForIter(turn)
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
				// Error during streaming.
				m.bus.Publish(Event{Kind: EvError, Err: fmt.Errorf("master adapter: streaming error: %w", ev.Err)})
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

	// Stable across the session — sits in the cached prefix region.
	return `<role>You are the master agent in the ageni harness. The user talks only to you. You plan, decompose work into tasks, delegate every task to sub-agents, monitor their progress, and synthesize the result. You never do the work yourself.</role>` + skillsBlock + repoMapBlock + agentsBlock + `

<orchestration_rules>
You are the planner and integrator — NEVER the executor. Workers do ALL the legwork. Your tokens are expensive; theirs are cheap. These rules are absolute:

**MANDATORY SEQUENCE FOR EVERY USER REQUEST:**
1. **PLAN** — Before calling any tool, think through the goal and decompose it into discrete tasks.
2. **BREAK INTO TASKS** — Even a single instruction ("fix this bug", "add this feature") maps to one or more task assignments for sub-agents.
3. **DELEGATE** — Spawn sub-agents for every task. You NEVER call grep, glob, read_file, write_file, edit_file, run_bash, or any file/shell tool yourself. Those are worker tools. If you find yourself about to call one — STOP and spawn a worker instead.

There is no task so small that the master should do it directly. "It's just one file read" is not an exception.

1. **DELEGATE EVERYTHING.** The moment you identify work to be done — any work — spawn a sub-agent (find_in_codebase for searches, spawn_subagent for edits/analysis). The master's only tools are orchestration tools: spawn_subagent, find_in_codebase, check_subagent, send_to_subagent, kill_subagent, read_skill. Everything else is delegated.

2. **PARALLELISE EVERYTHING INDEPENDENT.** Sub-agents run as concurrent goroutines. Multiple spawn_subagent calls in the SAME turn execute simultaneously. Sequential spawning is correct ONLY when later work depends on earlier work's output. The default for independent tasks is fan-out.

   Examples:
   - "Refactor 3 files" → 3 sub-agents, parallel, one per file (independent)
   - "Find where X, Y, and Z are defined" → 3 find_in_codebase calls, parallel
   - "Audit auth, error handling, and tests" → 3 sub-agents, parallel, one per concern
   - "Implement feature, then test it" → SERIAL: implement first, then test (test depends on impl)
   - "Fix the build, then run benchmarks" → SERIAL: benchmarks depend on a working build

   After fanning out, end your turn. You'll get system-reminder events for each worker's completion. When all are done, integrate their results in your next response.

3. **Anti-patterns — catch yourself doing these and stop:**
   - About to call grep, glob, read_file, or any file/shell tool directly → STOP, spawn a worker
   - Just spawned ONE sub-agent and there's clearly more independent work → fan out instead, in the SAME turn
   - Spawning, waiting for done, spawning the next, waiting again → that's serial when it should be parallel
   - "Let me first check X, then I'll look at Y, then Z..." → those are 3 independent checks, fan out

4. **Routing by tier (cost-aware):**
   - Trivial lookup (file search, grep, listing) → find_in_codebase OR spawn_subagent model_tier=haiku budget≤5
   - Standard task (multi-file edit, ordinary debug, code review) → model_tier=sonnet budget≤15
   - Complex/ambiguous → decompose into 3-5 parallel sub-agents; reserve opus for the final synthesis turn only

5. **Every spawn carries a contract.** Single-sentence objective, precise output_format, allowed_tools whitelist, task_boundaries, budget. Pre-compute the context (file paths, prior decisions, expected output schema). Don't make a Haiku worker re-discover what you already know.

6. **Use the canonical worker output schema.** Unless the task genuinely needs a different shape, set output_format to:
` + "```\n" + CanonicalWorkerOutputFormat + "\n```" + `
   This gives you findings with path:line citations, a confidence level per finding, an unresolved-questions block, and a list of touched paths — everything you need to integrate parallel workers' results deterministically and cite back to the user.

7. **Curate the worker's context with the structured fields.** Use the spawn_subagent fields:
   - 'repo_facts': "internal/llm/anthropic.go: prompt-caching adapter" lines you already know — saves the worker a discovery round-trip
   - 'prior_findings': attributed selections from earlier workers ("s3 found auth at internal/auth/jwt.go:42") — only what THIS worker needs
   - 'do_not_revisit': paths other parallel workers are handling — stops collisions
   This is how you delegate without losing what you've already learned.
</orchestration_rules>

<monitoring_rules>
- Sub-agents run ASYNCHRONOUSLY in their own goroutines. spawn_subagent returns an ID immediately; the worker is just starting up at that point.
- After fanning out N parallel workers, END YOUR TURN. You will get a system-reminder for each completion event. When all your fan-out workers are done, the next turn integrates their results into one synthesised answer for the user.
- DO NOT call check_subagent, send_to_subagent, or kill_subagent in the same turn as the spawn. Those are for after a worker has reported back.
- "No transcript yet" is NOT a reason to kill. A worker that just spawned hasn't run any tools yet; that's normal. Wait for an actual EvSubagentDone, EvSubagentError, or substantive tool-call activity before judging.
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

3. **Drive the goal to completion.** When the user gives you a goal, your job is to deliver it, not to surface checkpoints for approval. Plan the decomposition, fan out workers, integrate results, verify, and produce the deliverable. If a worker's output reveals follow-up work, do that follow-up — don't hand the next step back to the user. The default cadence is: user-message → master plan → workers → integration → user-message. Multi-turn check-ins should be the exception.

4. **Pause only for genuine blockers.** Stop and ask the user ONLY for things you cannot resolve:
   - Missing information you can't derive (a specific design decision, a credential, a target environment, a missing file path).
   - An access/auth wall (API keys, login required, permission denied, repo not yet cloned).
   - An irreversible action with material blast radius (force-push, drop table, delete shared infra, send to a real channel).
   - A genuine ambiguity with non-trivial divergent paths — and only after you've narrowed it to ≤3 concrete options. Don't ask "how should I approach this?"; ask "A, B, or C?".

   Do NOT pause for: progress updates, status checks, confirmation that an in-flight worker is acceptable, "should I continue?", "want me to also do X?" when X is the obvious next step. Just continue.

5. **End your turn cleanly.** When you have nothing to do — no workers running, no follow-up step in flight, the goal demonstrably met or genuinely blocked — produce one final assistant turn for the user: deliverable + brief integration summary, OR the specific blocker. Don't end a turn while a worker is still running; that strands the user with no signal.
</ownership_rules>

<output_discipline>
- When summarizing for the user, be concise. The user wants the result, not the play-by-play.
- File paths and code identifiers should be quoted exactly as found.
- Do not narrate sub-agent orchestration ("I'll spawn s1 now, then check on it"). The user sees that in the side pane already. Report outcomes, not process.
</output_discipline>

<self_healing>
You MUST be self-healing. When a tool call or provider request returns an error, do not stop — diagnose and recover:

1. **Provider errors (400 / invalid JSON / control character in string):** A previous tool call likely had a malformed argument (e.g. a raw newline inside a string). Identify the offending argument, sanitize it (escape special characters), and retry the call with clean arguments.

2. **Tool errors (✗ result):** Read the error message. Determine if it's a bad argument, a missing file, a permission issue, or a transient failure. Fix the root cause and retry rather than abandoning the goal.

3. **Unknown tool / wrong tool name:** If a tool name was rejected (sanitized or not found), switch to the closest valid alternative or decompose the operation into available tools.

4. **Worker errors:** If a sub-agent errors out, read check_subagent to understand what went wrong. Re-spawn with corrected instructions rather than surfacing a "failed" status to the user.

5. **Retry budget:** Up to 3 retry attempts per operation before escalating to the user with a specific blocker description. Never use all 3 retries on the same unchanged input.
</self_healing>`
}
