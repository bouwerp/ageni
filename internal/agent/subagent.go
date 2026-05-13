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
	UseSkill        string   `json:"use_skill,omitempty"` // master can pin a specific skill for the worker

	// Structured context — Anthropic's published lead-curates pattern. The
	// master pre-loads what each worker needs so workers don't re-discover
	// state and parallel workers don't collide. All optional.
	RepoFacts     []string `json:"repo_facts,omitempty"`     // "path:role" lines master already knows
	PriorFindings []string `json:"prior_findings,omitempty"` // attributed past worker outputs worth remembering
	DoNotRevisit  []string `json:"do_not_revisit,omitempty"` // paths/areas other workers are handling
}

// Subagent runs a single delegated task in its own goroutine.
type Subagent struct {
	ID      string
	Task    SubagentTask
	Model   string
	Adapter llm.Adapter

	bus     *Bus
	tools   *tools.Registry
	tracker *llm.Tracker

	// Budget on actual tool calls executed (not turns). Default 25.
	maxToolCalls int
	// Hard ceiling on turns regardless of budget — protects against an LLM
	// stuck in a no-tool-no-result loop. Should be > maxToolCalls.
	hardTurnCap int

	skillCatalog string

	// inbox carries follow-up user messages from the master via
	// send_to_subagent. The subagent loop drains this between turns and
	// appends each as a user-role message before continuing.
	inbox chan string

	// Retry / timeout policy. Defaults: turnTimeout 5min, maxRetries 3.
	turnTimeout time.Duration
	maxRetries  int

	mu         sync.Mutex
	status     SubagentStatus
	spawnedAt  time.Time
	transcript []string
	finalText  string
	cancel     context.CancelFunc
	scrubber   func(string) string // optional; redacts secrets from LLM text before storage
}

// SetScrubber installs a function applied to LLM-generated text before it is
// stored in message history or published to the bus.
func (s *Subagent) SetScrubber(f func(string) string) {
	s.mu.Lock()
	s.scrubber = f
	s.mu.Unlock()
}

func (s *Subagent) scrub(text string) string {
	s.mu.Lock()
	f := s.scrubber
	s.mu.Unlock()
	if f == nil || text == "" {
		return text
	}
	return f(text)
}

func NewSubagent(id string, task SubagentTask, adapter llm.Adapter, model string, registry *tools.Registry, bus *Bus, tracker *llm.Tracker, skillCatalog string) *Subagent {
	allowed := registry
	if len(task.AllowedTools) > 0 {
		allowed = registry.Subset(task.AllowedTools)
	}
	budget := task.BudgetToolCalls
	if budget <= 0 {
		budget = 40
	}
	return &Subagent{
		ID:           id,
		Task:         task,
		Model:        model,
		Adapter:      adapter,
		bus:          bus,
		tools:        allowed,
		tracker:      tracker,
		maxToolCalls: budget,
		hardTurnCap:  budget * 2,
		skillCatalog: skillCatalog,
		inbox:        make(chan string, 16),
		turnTimeout:  5 * time.Minute,
		maxRetries:   3,
		status:       StatusRunning,
		spawnedAt:    time.Now(),
	}
}

// Send injects a user-role message into the sub-agent's loop. The message is
// processed at the next turn boundary. Returns false if the inbox is full
// (slow consumer) or the sub-agent has already exited.
func (s *Subagent) Send(msg string) bool {
	if msg == "" {
		return false
	}
	switch s.Status() {
	case StatusError, StatusCancelled:
		return false
	}
	select {
	case s.inbox <- msg:
		return true
	default:
		return false
	}
}

func (s *Subagent) Status() SubagentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Elapsed returns how long the sub-agent has been running since it was spawned.
func (s *Subagent) Elapsed() time.Duration {
	return time.Since(s.spawnedAt).Truncate(time.Second)
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

	toolCallsUsed := 0
	wrappingUp := false

	for turn := 0; turn < s.hardTurnCap; turn++ {
		// Drain inbox messages from the master before this turn.
		messages = s.drainInbox(messages)

		req := llm.Request{
			Model:    s.Model,
			System:   system,
			Messages: messages,
		}
		// During wrap-up turns we strip tools so the model is forced to
		// produce its final text. Outside wrap-up, tools are available.
		if !wrappingUp {
			req.Tools = s.tools.Definitions()
		}

		assistantText, reasoningContent, toolCalls, err := s.runTurnWithRetry(ctx, req)
		if err != nil {
			s.fail(err)
			return
		}

		// Scrub any secrets that may have appeared in the LLM's text response
		// before they are stored in message history or final output.
		cleanText := s.scrub(assistantText)
		cleanCalls := toolCalls
		if s.scrubber != nil {
			cleanCalls = make([]llm.ToolCall, len(toolCalls))
			for i, tc := range toolCalls {
				if cleaned := s.scrub(string(tc.Arguments)); cleaned != string(tc.Arguments) {
					tc.Arguments = json.RawMessage(cleaned)
				}
				cleanCalls[i] = tc
			}
		}

		// Build assistant message + tool result messages for next turn.
		assistantMsg := llm.Message{Role: llm.RoleAssistant, Text: cleanText, ToolCalls: cleanCalls, ReasoningContent: reasoningContent}
		messages = append(messages, assistantMsg)

		if len(cleanCalls) == 0 {
			// Text-only response. If the master injected a follow-up while we
			// were generating, keep going instead of finishing.
			if pending := s.drainInbox(messages); len(pending) > len(messages) {
				messages = pending
				continue
			}
			s.mu.Lock()
			s.finalText = cleanText
			s.status = StatusDone
			s.mu.Unlock()
			s.appendTranscript("done")
			s.bus.Publish(Event{Kind: EvSubagentDone, SubagentID: s.ID, Text: cleanText})
			return
		}

		// Execute each tool call, one tool-result Message per call.
		for _, tc := range cleanCalls {
			result := s.tools.Execute(ctx, tc)
			s.appendTranscript(fmt.Sprintf("tool_done: %s%s", tc.Name, errMark(result.IsError)))
			s.bus.Publish(Event{Kind: EvSubagentToolDone, SubagentID: s.ID, ToolResult: &result})
			messages = append(messages, llm.Message{
				Role:        llm.RoleTool,
				ToolResults: []llm.ToolResult{result},
			})
			toolCallsUsed++
		}

		// Soft budget: when we've hit the cap, request a wrap-up turn next
		// instead of erroring out. The master gets a real <result>/<reasoning>
		// block from the worker rather than a useless "budget exhausted".
		if !wrappingUp && toolCallsUsed >= s.maxToolCalls {
			wrappingUp = true
			s.appendTranscript(fmt.Sprintf("budget exhausted (%d/%d tool calls); requesting wrap-up", toolCallsUsed, s.maxToolCalls))
			s.bus.Publish(Event{
				Kind:       EvSubagentRetry,
				SubagentID: s.ID,
				Text:       fmt.Sprintf("budget exhausted (%d tool calls used) — requesting wrap-up", toolCallsUsed),
			})
			messages = append(messages, llm.Message{
				Role: llm.RoleUser,
				Text: fmt.Sprintf("<system-reminder>\nYou have used your full tool-call budget (%d). Stop calling tools. Produce your final answer NOW: a single assistant turn with the requested <result>...</result> block followed by <reasoning>...</reasoning>. Summarise what you accomplished and what remains incomplete.\n</system-reminder>", s.maxToolCalls),
			})
		}
	}

	// Hard turn cap reached without the model producing a text-only turn.
	// Shouldn't happen in practice (wrap-up turn has Tools=nil so the model
	// has nothing to call), but error out cleanly if it does.
	s.mu.Lock()
	s.status = StatusError
	s.mu.Unlock()
	err := fmt.Errorf("hard turn cap (%d) reached without a final response", s.hardTurnCap)
	s.appendTranscript("error: " + err.Error())
	s.bus.Publish(Event{Kind: EvSubagentError, SubagentID: s.ID, Err: err})
}

func (s *Subagent) fail(err error) {
	// Cancellation is an explicit decision (user hit Esc, master killed the
	// worker, or the parent context was torn down on shutdown). It's not an
	// error — don't surface it as one. Mark the worker cancelled and emit a
	// terminal Done event with empty text so anything waiting on the bus
	// (find_in_codebase, master integration loop) unblocks cleanly.
	if errors.Is(err, context.Canceled) {
		s.mu.Lock()
		s.status = StatusCancelled
		s.mu.Unlock()
		s.appendTranscript("cancelled")
		s.bus.Publish(Event{Kind: EvSubagentDone, SubagentID: s.ID, Text: ""})
		return
	}
	s.mu.Lock()
	s.status = StatusError
	s.mu.Unlock()
	// Decorate the error so the TUI / session log can see what kind of
	// failure this was — context.DeadlineExceeded vs SDK errors look
	// different to the user.
	tag := classifyErr(err)
	wrapped := fmt.Errorf("[%s] %w", tag, err)
	s.appendTranscript("error(" + tag + "): " + err.Error())
	s.bus.Publish(Event{Kind: EvSubagentError, SubagentID: s.ID, Err: wrapped})
}

// classifyErr returns a short tag describing the error class for diagnostics.
func classifyErr(err error) string {
	if err == nil {
		return "nil"
	}
	if errors.Is(err, context.Canceled) {
		return "context-cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline-exceeded"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "unauthorized"):
		return "auth"
	case strings.Contains(msg, "404"):
		return "not-found"
	case strings.Contains(msg, "429"), strings.Contains(msg, "rate"):
		return "rate-limit"
	case strings.Contains(msg, "500"), strings.Contains(msg, "502"),
		strings.Contains(msg, "503"), strings.Contains(msg, "504"):
		return "server-error"
	case strings.Contains(msg, "connection"), strings.Contains(msg, "timeout"), strings.Contains(msg, "eof"):
		return "network"
	}
	return "other"
}

func errMark(b bool) string {
	if b {
		return " [error]"
	}
	return ""
}

// maxIdleRetries is the number of times a sub-agent will retry a turn after
// the stream idle watchdog fires before giving up and failing the sub-agent.
const maxIdleRetries = 2

// runTurnWithRetry runs one LLM turn with a per-turn timeout and retries on
// transient failures (deadline, network, rate-limit, 5xx, idle watchdog).
// Returns the assistant text + any tool calls, or a terminal error.
func (s *Subagent) runTurnWithRetry(parent context.Context, req llm.Request) (string, string, []llm.ToolCall, error) {
	var lastErr error
	idleRetries := 0
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		if parent.Err() != nil {
			return "", "", nil, parent.Err()
		}
		ctx, cancel := context.WithTimeout(parent, s.turnTimeout)
		text, rc, calls, err := s.runOneTurn(ctx, req)
		cancel()
		if err == nil {
			return text, rc, calls, nil
		}
		lastErr = err

		// Don't retry user-cancelled conditions.
		if errors.Is(err, context.Canceled) {
			return "", "", nil, err
		}

		// Idle watchdog fired — the model went silent. Retry with a nudge
		// so the provider gets a fresh request rather than a stale one.
		if llm.IsStreamIdle(err) {
			idleRetries++
			msg := fmt.Sprintf("model idle (attempt %d/%d) — retrying", idleRetries, maxIdleRetries)
			s.appendTranscript(msg)
			s.bus.Publish(Event{Kind: EvSubagentRetry, SubagentID: s.ID, Text: msg})
			if idleRetries >= maxIdleRetries {
				return "", "", nil, fmt.Errorf("model not responding after %d retries — master should re-spawn this sub-agent", maxIdleRetries)
			}
			// Brief pause before re-issuing the request.
			select {
			case <-parent.Done():
				return "", "", nil, parent.Err()
			case <-time.After(3 * time.Second):
			}
			// Don't advance attempt counter — idle retries are separate.
			attempt--
			continue
		}

		if !isTransientErr(err) || attempt == s.maxRetries {
			return "", "", nil, err
		}

		// Exponential backoff with light jitter.
		wait := time.Duration(1<<attempt) * time.Second // 1s, 2s, 4s
		s.appendTranscript(fmt.Sprintf("retry %d/%d in %s: %v", attempt+1, s.maxRetries, wait, err))
		s.bus.Publish(Event{Kind: EvSubagentRetry, SubagentID: s.ID, Text: err.Error()})
		select {
		case <-parent.Done():
			return "", "", nil, parent.Err()
		case <-time.After(wait):
		}
	}
	return "", "", nil, lastErr
}

// runOneTurn does a single Stream call and accumulates the result.
func (s *Subagent) runOneTurn(ctx context.Context, req llm.Request) (string, string, []llm.ToolCall, error) {
	// Mirror the master: emit a turn-start event so the TUI can show the
	// sub-agent as "thinking" while the LLM call is in flight, distinct
	// from the running-a-tool state.
	s.bus.Publish(Event{Kind: EvSubagentTurnStart, SubagentID: s.ID})
	stream, err := s.Adapter.Stream(ctx, req)
	if err != nil {
		return "", "", nil, err
	}
	// Wrap with an idle watchdog so a silent TCP connection or a stalled
	// provider doesn't leave the sub-agent stuck in "thinking" forever.
	stream = llm.WatchdogStream(stream)
	var text strings.Builder
	var calls []llm.ToolCall
	var reasoningContent string
	for ev := range stream {
		switch ev.Type {
		case llm.StreamEventText:
			text.WriteString(ev.TextDelta)
			s.bus.Publish(Event{Kind: EvSubagentText, SubagentID: s.ID, Text: ev.TextDelta})
		case llm.StreamEventToolCall:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
				s.appendTranscript(fmt.Sprintf("tool_call: %s", ev.ToolCall.Name))
				s.bus.Publish(Event{Kind: EvSubagentToolCall, SubagentID: s.ID, ToolCall: ev.ToolCall})
			}
		case llm.StreamEventError:
			return text.String(), "", calls, ev.Err
		case llm.StreamEventDone:
			if ev.Usage != nil {
				s.tracker.Add("subagent:"+s.ID, s.Model, *ev.Usage)
				s.bus.Publish(Event{Kind: EvSubagentUsage, SubagentID: s.ID, Usage: ev.Usage})
			}
			reasoningContent = ev.ReasoningContent
		}
	}
	if ctx.Err() != nil {
		return text.String(), "", calls, ctx.Err()
	}
	return text.String(), reasoningContent, calls, nil
}

// drainInbox appends any pending messages from the master as user-role
// messages and notifies the bus so the UI can reflect the injection.
func (s *Subagent) drainInbox(messages []llm.Message) []llm.Message {
	for {
		select {
		case msg := <-s.inbox:
			messages = append(messages, llm.Message{
				Role: llm.RoleUser,
				Text: "<master_message>" + msg + "</master_message>",
			})
			s.appendTranscript("inbox: " + msg)
			s.bus.Publish(Event{Kind: EvSubagentInbox, SubagentID: s.ID, Text: msg})
		default:
			return messages
		}
	}
}

// isTransientErr returns true for errors that are likely to succeed on
// retry: deadline exceeded, rate limits, 5xx responses, network errors.
func isTransientErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	transientHints := []string{
		"timeout", "timed out",
		"connection refused", "connection reset", "broken pipe",
		"eof", "unexpected eof",
		"429", "rate limit", "rate-limit",
		"500", "502", "503", "504",
		"overloaded", "service unavailable",
		"temporary failure",
	}
	for _, h := range transientHints {
		if strings.Contains(msg, h) {
			return true
		}
	}
	return false
}

func (s *Subagent) systemPrompt() string {
	skillsBlock := ""
	if s.skillCatalog != "" {
		skillsBlock = "\n\n<available_skills>\n" + s.skillCatalog + "\n\nCall read_skill(name=\"...\") to load a skill's full instructions when one matches your task.\n</available_skills>"
	}
	// XML-tagged for Claude (no-op for OpenAI but harmless).
	return `<role>You are a sub-agent in the ageni harness. You execute one focused task delegated by a master agent and return a structured result.</role>` + skillsBlock + `

<rules>
- Stay strictly within the task boundaries you were given.
- Use only the tools listed in <allowed_tools>; do not request others.
- Respect the tool-call budget. If you cannot complete within budget, return what you have plus a clear blocker description.
- Final response: produce exactly one assistant turn that contains a <result>...</result> block matching the requested output_format, followed by a <reasoning>...</reasoning> block summarizing what you did. No tool calls in the final turn.
- Do not invent file paths, function names, or APIs. If you don't know, say so.
- If the master named a specific skill in <use_skill>, call read_skill on it first and apply its procedures.
</rules>

<self_healing>
When a tool returns an error, do not stop — diagnose and recover autonomously:
- Bad argument / invalid JSON: fix the argument and retry.
- File not found: check if the path exists with list_dir or glob, then use the correct path.
- Permission or transient error: retry up to 2 more times before including it as a blocker in your <result>.
- Unknown tool: use the closest available alternative from <allowed_tools>.
Never ask the master or the user for help with recoverable errors — handle them yourself.
</self_healing>`
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
	if s.Task.UseSkill != "" {
		sb.WriteString("<use_skill>" + s.Task.UseSkill + "</use_skill>\n")
	}
	if len(s.Task.RepoFacts) > 0 {
		sb.WriteString("<repo_facts>\n")
		for _, f := range s.Task.RepoFacts {
			sb.WriteString("- " + f + "\n")
		}
		sb.WriteString("</repo_facts>\n")
	}
	if len(s.Task.PriorFindings) > 0 {
		sb.WriteString("<prior_findings>\n")
		for _, f := range s.Task.PriorFindings {
			sb.WriteString("- " + f + "\n")
		}
		sb.WriteString("</prior_findings>\n")
	}
	if len(s.Task.DoNotRevisit) > 0 {
		sb.WriteString("<do_not_revisit>\n")
		for _, f := range s.Task.DoNotRevisit {
			sb.WriteString("- " + f + "\n")
		}
		sb.WriteString("</do_not_revisit>\n")
		sb.WriteString("(other workers are handling these — stay clear)\n")
	}
	if s.Task.Context != "" {
		sb.WriteString("<context>" + s.Task.Context + "</context>\n")
	}
	sb.WriteString("</task>\n\nBegin.")
	return sb.String()
}
