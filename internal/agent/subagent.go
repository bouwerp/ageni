package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/models"
	"github.com/bouwerp/ageni/internal/tools"
)

type SubagentStatus string

const (
	StatusRunning   SubagentStatus = "running"
	StatusPaused    SubagentStatus = "paused"
	StatusIdle      SubagentStatus = "idle"
	StatusDone      SubagentStatus = "done"
	StatusError     SubagentStatus = "error"
	StatusCancelled SubagentStatus = "cancelled"
)

const (
	subagentContextBudgetTokens = 2_500
	subagentContextMaxItems     = 8
	subagentContextItemMaxChars = 220
)

// SubagentTask is the contract every spawn must specify. Vague spawns are
// rejected upstream (see the spawn_subagent tool schema).
type SubagentTask struct {
	Objective       string   `json:"objective"`
	OutputFormat    string   `json:"output_format"`
	AllowedTools    []string `json:"allowed_tools"`
	TaskBoundaries  string   `json:"task_boundaries"`
	BudgetToolCalls int      `json:"budget_tool_calls"`
	ModelTier       string   `json:"model_tier"` // haiku | sonnet | opus (legacy) or fast | mid | flagship
	Context         string   `json:"context,omitempty"`
	UseSkill        string   `json:"use_skill,omitempty"` // master can pin a specific skill for the worker

	// RequiredCaps is an optional list of capabilities the selected model must
	// have (e.g. ["vision"]). When non-empty, the manager may upgrade the tier
	// to find a model with the required capabilities.
	RequiredCaps []string `json:"required_capabilities,omitempty"`

	// Structured context — Anthropic's published lead-curates pattern. The
	// master pre-loads what each worker needs so workers don't re-discover
	// state and parallel workers don't collide. All optional.
	RepoFacts     []string `json:"repo_facts,omitempty"`     // "path:role" lines master already knows
	PriorFindings []string `json:"prior_findings,omitempty"` // attributed past worker outputs worth remembering
	DoNotRevisit  []string `json:"do_not_revisit,omitempty"` // paths/areas other workers are handling

	// Role is an optional predefined role name (e.g. "architect", "qa-engineer").
	// When set, role defaults (tier, budget, use_skill, required_caps,
	// task_boundaries, RoleSystemAddendum) are applied before auto-detection.
	// Explicit spawn params always override role defaults.
	Role string `json:"role,omitempty"`

	// RoleSystemAddendum is injected into the subagent system prompt after the
	// base instructions. Set automatically when Role is resolved; not intended
	// to be set directly by the master.
	RoleSystemAddendum string `json:"-"`

	// TimeoutMinutes overrides the default total-runtime budget for this worker.
	// When 0 (omitted), the system default (10 minutes) is used.
	// Useful for long-running tasks that are known upfront to exceed the default.
	TimeoutMinutes float64 `json:"timeout_minutes,omitempty"`
}

func (t *SubagentTask) UnmarshalJSON(data []byte) error {
	type Alias SubagentTask
	aux := &struct {
		*Alias
		AllowedTools json.RawMessage `json:"allowed_tools"`
	}{
		Alias: (*Alias)(t),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if len(aux.AllowedTools) > 0 {
		var arr []string
		if err := json.Unmarshal(aux.AllowedTools, &arr); err == nil {
			t.AllowedTools = arr
			return nil
		}
		var str string
		if err := json.Unmarshal(aux.AllowedTools, &str); err == nil {
			if str == "" {
				t.AllowedTools = nil
				return nil
			}
			str = strings.TrimSpace(str)
			if strings.HasPrefix(str, "[") && strings.HasSuffix(str, "]") {
				str = str[1 : len(str)-1]
				str = strings.ReplaceAll(str, "'", "\"")
				var innerArr []string
				if err := json.Unmarshal([]byte("["+str+"]"), &innerArr); err == nil {
					t.AllowedTools = innerArr
					return nil
				}
			}
			parts := strings.Split(str, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(strings.Trim(parts[i], `"'`))
			}
			var filtered []string
			for _, p := range parts {
				if p != "" {
					filtered = append(filtered, p)
				}
			}
			t.AllowedTools = filtered
			return nil
		}
		return fmt.Errorf("allowed_tools: cannot unmarshal %s into []string", string(aux.AllowedTools))
	}
	return nil
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
	roleCatalog  string
	memBlock     string // snapshot of memories at spawn time, injected into system prompt
	repoMap      string // snapshot of repository map at spawn time, injected into system prompt

	// capabilities lists the model capabilities of this subagent's model
	// (e.g. "vision", "reasoning"). Injected into the system prompt so the
	// subagent knows which tools it can legitimately call.
	capabilities []string

	// inbox carries follow-up user messages from the master via
	// send_to_subagent. The subagent loop drains this between turns and
	// appends each as a user-role message before continuing.
	inbox chan string

	// Retry / timeout policy. Defaults: turnTimeout 5min, maxRetries 3, maxTotalRuntime 10min.
	// maxTotalRuntime is overridden by SubagentTask.TimeoutMinutes when set.
	turnTimeout     time.Duration
	maxRetries      int
	maxTotalRuntime time.Duration

	mu            sync.Mutex
	status        SubagentStatus
	spawnedAt     time.Time
	transcript    []string
	finalText       string
	cancel          context.CancelFunc
	scrubber        func(string) string // optional; redacts secrets from LLM text before storage
	correlationID   string
	paused          bool
	pauseCond       *sync.Cond
	lastInputTokens int
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

func NewSubagent(id string, task SubagentTask, adapter llm.Adapter, model string, registry *tools.Registry, bus *Bus, tracker *llm.Tracker, skillCatalog string, roleCatalog string, memBlock string, repoMap string, caps []string, correlationID string) *Subagent {
	allowed := registry
	if len(task.AllowedTools) > 0 {
		allowed = registry.Subset(task.AllowedTools)
		hasMutation := false
		for _, name := range allowed.Names() {
			if isMutationToolName(name) {
				hasMutation = true
				break
			}
		}
		if hasMutation || taskLikelyNeedsMutation(task) {
			if _, hasReadFile := allowed.Get("read_file"); !hasReadFile {
				if _, ok := registry.Get("read_file"); ok {
					task.AllowedTools = append(task.AllowedTools, "read_file")
					allowed = registry.Subset(task.AllowedTools)
				}
			}
		}
	}
	var filtered []string
	for _, name := range allowed.Names() {
		if name != "write_file" && name != "transactional_edit" {
			filtered = append(filtered, name)
		}
	}
	allowed = allowed.Subset(filtered)
	budget := task.BudgetToolCalls
	if budget <= 0 {
		isLocal := adapter.Provider() == "llamacpp" || adapter.Provider() == "llamacpp-fleet"
		if isLocal {
			budget = 300
		} else {
			budget = 200
		}
	}
	totalRuntime := 10 * time.Minute
	if task.TimeoutMinutes > 0 {
		totalRuntime = time.Duration(task.TimeoutMinutes * float64(time.Minute))
	}
	sub := &Subagent{
		ID:              id,
		Task:            task,
		Model:           model,
		Adapter:         adapter,
		bus:             bus,
		tools:           allowed,
		tracker:         tracker,
		maxToolCalls:    budget,
		hardTurnCap:     budget * 2,
		skillCatalog:    skillCatalog,
		roleCatalog:     roleCatalog,
		memBlock:        memBlock,
		repoMap:         repoMap,
		capabilities:    append([]string(nil), caps...),
		inbox:           make(chan string, 16),
		turnTimeout:     5 * time.Minute,
		maxRetries:      3,
		maxTotalRuntime: totalRuntime,
		status:          StatusRunning,
		spawnedAt:       time.Now(),
		correlationID:   correlationID,
	}
	sub.pauseCond = sync.NewCond(&sub.mu)
	return sub
}

func (s *Subagent) publish(ev Event) {
	ev.CorrelationID = s.correlationID
	s.bus.Publish(ev)
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

func (s *Subagent) Pause() bool {
	s.mu.Lock()
	emit := false
	switch s.status {
	case StatusDone, StatusError, StatusCancelled:
		s.mu.Unlock()
		return false
	case StatusPaused:
		s.mu.Unlock()
		return true
	}
	s.paused = true
	s.status = StatusPaused
	emit = true
	s.mu.Unlock()
	if emit {
		s.appendTranscript("paused")
		s.publish(Event{Kind: EvSubagentPaused, SubagentID: s.ID})
	}
	return true
}

func (s *Subagent) Resume() bool {
	s.mu.Lock()
	if !s.paused {
		s.mu.Unlock()
		return false
	}
	s.paused = false
	s.status = StatusRunning
	s.pauseCond.Broadcast()
	s.mu.Unlock()
	s.appendTranscript("resumed")
	s.publish(Event{Kind: EvSubagentResumed, SubagentID: s.ID})
	return true
}

func (s *Subagent) waitIfPaused(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.paused {
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				s.mu.Lock()
				s.pauseCond.Broadcast()
				s.mu.Unlock()
			case <-done:
			}
		}()
		s.pauseCond.Wait()
		close(done)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
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
	ctx, cancel := context.WithTimeout(parent, s.maxTotalRuntime)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	system := s.systemPrompt()
	messages := []llm.Message{
		{Role: llm.RoleUser, Text: s.userPrompt()},
	}

	s.publish(Event{
		Kind:          EvSubagentSpawn,
		SubagentID:    s.ID,
		SubagentTask:  s.Task.Objective,
		SubagentModel: s.Model,
	})

	toolCallsUsed := 0
	wrappingUp := false
	allToolDefs := s.tools.Definitions()
	inspectionComplete := !requiresInspectionFirst(s.Task, allToolDefs)
	pendingInspectionAttempts := 0
	pendingVerificationReminder := ""
	pendingVerificationAttempts := 0
	pendingEditRecoveryReminder := ""
	pendingEditRecoveryAttempts := 0
	mutationAttempted := false
	mutationSucceeded := false
	pendingMutationAttempts := 0

	for turn := 0; turn < s.hardTurnCap; turn++ {
		if err := s.waitIfPaused(ctx); err != nil {
			s.fail(err)
			return
		}
		// Drain inbox messages from the master before this turn.
		messages = s.drainInbox(messages)

		s.mu.Lock()
		tokens := s.lastInputTokens
		s.mu.Unlock()
		threshold := 16000
		if isLlamaCPP(s.Adapter) {
			threshold = 4000
		}
		if tokens >= threshold {
			messages = trimSubagentHistory(messages, 3)
			s.mu.Lock()
			s.lastInputTokens = 0
			s.mu.Unlock()
			s.appendTranscript("trimmed oldest conversation history to fit model context window")
		}

		req := llm.Request{
			Model:    s.Model,
			System:   system,
			Messages: messages,
		}
		// During wrap-up turns we strip tools so the model is forced to
		// produce its final text. Outside wrap-up, tools are available.
		if !wrappingUp {
			req.Tools = allToolDefs
			if !inspectionComplete {
				req.Tools = filterToolDefs(allToolDefs, isInspectionToolName)
				if len(req.Tools) == 0 {
					req.Tools = allToolDefs
					inspectionComplete = true
				}
			}
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
		assistantMsg := llm.Message{Role: llm.RoleAssistant, Text: cleanText, ToolCalls: cleanCalls, ReasoningContent: s.scrub(reasoningContent)}
		messages = append(messages, assistantMsg)

		if len(cleanCalls) == 0 {
			if !inspectionComplete && !wrappingUp && toolCallsUsed < s.maxToolCalls && pendingInspectionAttempts < maxInspectionReminders {
				pendingInspectionAttempts++
				msg := buildInspectionReminder(pendingInspectionAttempts, maxInspectionReminders)
				s.appendTranscript("inspection reminder: inspect before mutating files")
				s.publish(Event{
					Kind:       EvSubagentRetry,
					SubagentID: s.ID,
					Text:       "inspect current code before mutating files",
				})
				messages = append(messages, llm.Message{
					Role: llm.RoleUser,
					Text: msg,
				})
				continue
			}
			if pendingVerificationReminder != "" && !wrappingUp && toolCallsUsed < s.maxToolCalls && pendingVerificationAttempts < maxVerificationReminders {
				pendingVerificationAttempts++
				msg := buildVerificationReminder(pendingVerificationReminder, pendingVerificationAttempts, maxVerificationReminders)
				s.appendTranscript("verification reminder: unresolved lint/test issues")
				s.publish(Event{
					Kind:       EvSubagentRetry,
					SubagentID: s.ID,
					Text:       "unresolved verification issues — fix before finalizing",
				})
				messages = append(messages, llm.Message{
					Role: llm.RoleUser,
					Text: msg,
				})
				continue
			}
			if pendingEditRecoveryReminder != "" && !wrappingUp && toolCallsUsed < s.maxToolCalls && pendingEditRecoveryAttempts < maxEditRecoveryReminders {
				pendingEditRecoveryAttempts++
				msg := buildEditRecoveryReminder(pendingEditRecoveryReminder, pendingEditRecoveryAttempts, maxEditRecoveryReminders)
				s.appendTranscript("edit recovery reminder: prefer apply_diff after brittle edit failure")
				s.publish(Event{
					Kind:       EvSubagentRetry,
					SubagentID: s.ID,
					Text:       "brittle edit failed — switch to apply_diff before finalizing",
				})
				messages = append(messages, llm.Message{
					Role: llm.RoleUser,
					Text: msg,
				})
				continue
			}
			if taskLikelyNeedsMutation(s.Task) && mutationAttempted && !mutationSucceeded && !wrappingUp && toolCallsUsed < s.maxToolCalls && pendingMutationAttempts < maxMutationReminders {
				pendingMutationAttempts++
				msg := buildMutationReminder(pendingMutationAttempts, maxMutationReminders)
				s.appendTranscript("mutation reminder: no successful edits made yet")
				s.publish(Event{
					Kind:       EvSubagentRetry,
					SubagentID: s.ID,
					Text:       "no successful edits made yet — apply your changes before finalizing",
				})
				messages = append(messages, llm.Message{
					Role: llm.RoleUser,
					Text: msg,
				})
				continue
			}
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
			s.publish(Event{Kind: EvSubagentDone, SubagentID: s.ID, Text: cleanText})
			return
		}

		// Execute each tool call, one tool-result Message per call.
		turnResults := make([]llm.ToolResult, 0, len(cleanCalls))
		for _, tc := range cleanCalls {
			if ctx.Err() != nil {
				break
			}
			if err := s.waitIfPaused(ctx); err != nil {
				s.fail(err)
				return
			}
			result := s.tools.Execute(ctx, tc)
			if isMutationToolName(tc.Name) {
				mutationAttempted = true
				if !result.IsError {
					mutationSucceeded = true
				}
			}
			s.appendTranscript(fmt.Sprintf("tool_done: %s%s", tc.Name, errMark(result.IsError)))
			s.publish(Event{Kind: EvSubagentToolDone, SubagentID: s.ID, ToolResult: &result})
			turnResults = append(turnResults, result)
			messages = append(messages, llm.Message{
				Role:        llm.RoleTool,
				ToolResults: []llm.ToolResult{result},
			})
			toolCallsUsed++
		}
		if !inspectionComplete && usedInspectionTool(cleanCalls) {
			inspectionComplete = true
			pendingInspectionAttempts = 0
		}
		pendingVerificationReminder, pendingVerificationAttempts = updateVerificationState(
			pendingVerificationReminder,
			pendingVerificationAttempts,
			cleanCalls,
			turnResults,
		)
		pendingEditRecoveryReminder, pendingEditRecoveryAttempts = updateEditRecoveryState(
			pendingEditRecoveryReminder,
			pendingEditRecoveryAttempts,
			cleanCalls,
			turnResults,
		)

		// Soft budget: when we've hit the cap, request a wrap-up turn next
		// instead of erroring out. The master gets a real <result>/<reasoning>
		// block from the worker rather than a useless "budget exhausted".
		if !wrappingUp && toolCallsUsed >= s.maxToolCalls {
			wrappingUp = true
			s.appendTranscript(fmt.Sprintf("budget exhausted (%d/%d tool calls); requesting wrap-up", toolCallsUsed, s.maxToolCalls))
			s.publish(Event{
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
	s.publish(Event{Kind: EvSubagentError, SubagentID: s.ID, Err: err})
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
		s.publish(Event{Kind: EvSubagentDone, SubagentID: s.ID, Text: ""})
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
	s.appendTranscript("error(" + tag + "): " + llm.ErrorSummary(err))
	s.publish(Event{Kind: EvSubagentError, SubagentID: s.ID, Err: wrapped})
}

// classifyErr returns a short tag describing the error class for diagnostics.
func classifyErr(err error) string {
	if err == nil {
		return "nil"
	}
	return llm.ErrorClassTag(err)
}

func errMark(b bool) string {
	if b {
		return " [error]"
	}
	return ""
}

const maxInspectionReminders = 2
const maxVerificationReminders = 2
const maxEditRecoveryReminders = 2
const maxMutationReminders = 2

var inspectionToolNames = map[string]struct{}{
	"read_file":       {},
	"list_dir":        {},
	"glob":            {},
	"grep":            {},
	"search_symbols":  {},
	"find_references": {},
	"git_status":      {},
	"git_diff":        {},
	"git_log":         {},
	"pkg_info":        {},
	"web_fetch":       {},
	"web_search":      {},
	"github":          {},
	"view_image":      {},
}

var mutationToolNames = map[string]struct{}{
	"apply_diff":         {},
	"edit_file":          {},
	"multi_edit":         {},
	"transactional_edit": {},
	"write_file":         {},
	"make_dir":           {},
	"move_file":          {},
	"delete_file":        {},
}

var verificationEditTools = map[string]struct{}{
	"apply_diff":         {},
	"edit_file":          {},
	"multi_edit":         {},
	"transactional_edit": {},
	"write_file":         {},
}

var brittleExactMatchEditTools = map[string]struct{}{
	"edit_file":          {},
	"multi_edit":         {},
	"transactional_edit": {},
}

func updateVerificationState(previous string, attempts int, calls []llm.ToolCall, results []llm.ToolResult) (string, int) {
	sawRelevantTool := false
	var issues []string
	for i, call := range calls {
		if i >= len(results) {
			break
		}
		switch {
		case call.Name == "run_tests":
			sawRelevantTool = true
			issues = append(issues, unresolvedTestIssues(results[i].Content)...)
		case call.Name == "run_bash" && isTestLikeRunBash(call.Arguments):
			sawRelevantTool = true
			issues = append(issues, unresolvedTestIssues(results[i].Content)...)
		case call.Name == "run_bash" && isBuildLikeRunBash(call.Arguments):
			sawRelevantTool = true
			issues = append(issues, unresolvedBuildIssues(results[i].Content)...)
		case isVerificationEditTool(call.Name):
			sawRelevantTool = true
			issues = append(issues, unresolvedLintIssues(results[i].Content)...)
		}
	}
	if len(issues) > 0 {
		return formatVerificationIssues(issues), 0
	}
	if sawRelevantTool {
		return "", 0
	}
	return previous, attempts
}

func isVerificationEditTool(name string) bool {
	_, ok := verificationEditTools[name]
	return ok
}

func isInspectionToolName(name string) bool {
	_, ok := inspectionToolNames[name]
	return ok
}

func isMutationToolName(name string) bool {
	_, ok := mutationToolNames[name]
	return ok
}

func isBrittleExactMatchEditTool(name string) bool {
	_, ok := brittleExactMatchEditTools[name]
	return ok
}

func filterToolDefs(defs []llm.ToolDef, keep func(string) bool) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(defs))
	for _, def := range defs {
		if keep(def.Name) {
			out = append(out, def)
		}
	}
	return out
}

func requiresInspectionFirst(task SubagentTask, defs []llm.ToolDef) bool {
	if !taskLikelyNeedsMutation(task) {
		return false
	}
	hasInspection := false
	hasMutation := false
	for _, def := range defs {
		if isInspectionToolName(def.Name) {
			hasInspection = true
		}
		if isMutationToolName(def.Name) {
			hasMutation = true
		}
	}
	return hasInspection && hasMutation
}

func taskLikelyNeedsMutation(task SubagentTask) bool {
	if strings.Contains(strings.ToLower(task.TaskBoundaries), "do not edit") {
		return false
	}
	text := strings.ToLower(strings.Join([]string{task.Objective, task.Context}, " "))
	switch {
	case strings.Contains(text, "fix"),
		strings.Contains(text, "edit"),
		strings.Contains(text, "modify"),
		strings.Contains(text, "update"),
		strings.Contains(text, "refactor"),
		strings.Contains(text, "rename"),
		strings.Contains(text, "implement"),
		strings.Contains(text, "add"),
		strings.Contains(text, "remove"),
		strings.Contains(text, "rewrite"),
		strings.Contains(text, "change"),
		strings.Contains(text, "patch"):
		return true
	default:
		return false
	}
}

func usedInspectionTool(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if isInspectionToolName(call.Name) {
			return true
		}
	}
	return false
}

func unresolvedLintIssues(content string) []string {
	lines := strings.Split(content, "\n")
	seen := make(map[string]struct{}, len(lines))
	issues := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "[lint]") || strings.Contains(trimmed, ": ok") {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		issues = append(issues, trimmed)
	}
	return issues
}

func unresolvedTestIssues(content string) []string {
	firstLine := strings.TrimSpace(strings.SplitN(content, "\n", 2)[0])
	if firstLine == "" {
		return nil
	}
	if strings.HasPrefix(firstLine, "[exit ") && firstLine != "[exit 0]" {
		return []string{"[tests] " + firstLine}
	}
	if strings.HasPrefix(content, "go test: ") && !strings.Contains(firstLine, "fail=0") {
		return []string{"[tests] " + firstLine}
	}
	return nil
}

func unresolvedBuildIssues(content string) []string {
	firstLine := strings.TrimSpace(strings.SplitN(content, "\n", 2)[0])
	if firstLine == "" {
		return nil
	}
	if strings.HasPrefix(firstLine, "[exit ") && firstLine != "[exit 0]" {
		return []string{"[build] " + firstLine}
	}
	return nil
}

func updateEditRecoveryState(previous string, attempts int, calls []llm.ToolCall, results []llm.ToolResult) (string, int) {
	sawRelevantTool := false
	var issues []string
	for i, call := range calls {
		if i >= len(results) {
			break
		}
		if !isVerificationEditTool(call.Name) {
			continue
		}
		sawRelevantTool = true
		if issue := unresolvedEditRecoveryIssue(call.Name, results[i]); issue != "" {
			issues = append(issues, issue)
		}
	}
	if len(issues) > 0 {
		return formatVerificationIssues(issues), 0
	}
	if sawRelevantTool {
		return "", 0
	}
	return previous, attempts
}

func unresolvedEditRecoveryIssue(toolName string, result llm.ToolResult) string {
	if !result.IsError || !isBrittleExactMatchEditTool(toolName) {
		return ""
	}
	firstLine := strings.TrimSpace(strings.SplitN(result.Content, "\n", 2)[0])
	if firstLine == "" || !strings.Contains(strings.ToLower(firstLine), "prefer apply_diff") {
		return ""
	}
	return fmt.Sprintf("[edit-recovery] %s failed: %s", toolName, firstLine)
}

func runBashCommand(args json.RawMessage) string {
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ""
	}
	return strings.ToLower(p.Command)
}

func isTestLikeRunBash(args json.RawMessage) bool {
	command := runBashCommand(args)
	if command == "" {
		return false
	}
	switch {
	case strings.Contains(command, "go test"),
		strings.Contains(command, "pytest"),
		strings.Contains(command, "cargo test"),
		strings.Contains(command, "npm test"),
		strings.Contains(command, "pnpm test"),
		strings.Contains(command, "yarn test"),
		strings.Contains(command, "bun test"),
		strings.Contains(command, "mvn test"),
		strings.Contains(command, "gradlew test"):
		return true
	default:
		return false
	}
}

func isBuildLikeRunBash(args json.RawMessage) bool {
	command := runBashCommand(args)
	if command == "" {
		return false
	}
	switch {
	case strings.Contains(command, "go build"),
		strings.Contains(command, "go install"),
		strings.Contains(command, "tsc"),
		strings.Contains(command, "cargo check"),
		strings.Contains(command, "cargo build"),
		strings.Contains(command, "npm run build"),
		strings.Contains(command, "pnpm build"),
		strings.Contains(command, "pnpm run build"),
		strings.Contains(command, "yarn build"),
		strings.Contains(command, "bun build"),
		strings.Contains(command, "bun run build"),
		strings.Contains(command, "mvn compile"),
		strings.Contains(command, "mvn package"),
		strings.Contains(command, "mvn test-compile"),
		strings.Contains(command, "gradlew build"),
		strings.Contains(command, "gradlew classes"),
		strings.Contains(command, "gradlew assemble"):
		return true
	default:
		return false
	}
}

func formatVerificationIssues(issues []string) string {
	if len(issues) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, issue := range issues {
		sb.WriteString("- ")
		sb.WriteString(issue)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func buildVerificationReminder(issues string, attempt, max int) string {
	return fmt.Sprintf(
		"<system-reminder>\nThe most recent verification-related tool results still show unresolved issues:\n%s\nDo not produce your final <result> yet. Make another relevant tool call to fix the problem or rerun verification, then continue. If you cannot resolve it within the remaining budget, explain the blocker explicitly in your final <result>.\nThis is reminder %d of %d.\n</system-reminder>",
		issues,
		attempt,
		max,
	)
}

func buildInspectionReminder(attempt, max int) string {
	return fmt.Sprintf(
		"<system-reminder>\nThis task appears to require code changes. Inspect the current code before mutating files: make a read/search tool call first (for example read_file, grep, glob, search_symbols, or git_diff), then continue with edits.\nDo not produce your final <result> yet.\nThis is reminder %d of %d.\n</system-reminder>",
		attempt,
		max,
	)
}

func buildEditRecoveryReminder(issues string, attempt, max int) string {
	return fmt.Sprintf(
		"<system-reminder>\nThe most recent exact-match edit tool failed in a way that suggests a more robust edit backend:\n%s\nDo not produce your final <result> yet. Switch to apply_diff for the next edit attempt, or explain explicitly in your final <result> why you cannot complete the change with the available tools.\nThis is reminder %d of %d.\n</system-reminder>",
		issues,
		attempt,
		max,
	)
}

func buildMutationReminder(attempt, max int) string {
	return fmt.Sprintf(
		"<system-reminder>\nYour objective requires making code changes (e.g. fix, edit, implement, add, etc.), but you have not successfully applied any changes yet (or all your edit tool calls returned errors).\nBefore producing your final <result>, you must successfully apply your changes using the editing tools (such as apply_diff or edit_file).\nIf you are unable to apply the changes, describe the specific blockers or errors in your final response, but do not claim success.\nThis is reminder %d of %d.\n</system-reminder>",
		attempt,
		max,
	)
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
			s.publish(Event{Kind: EvSubagentRetry, SubagentID: s.ID, Text: msg})
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
		s.publish(Event{Kind: EvSubagentRetry, SubagentID: s.ID, Text: err.Error()})
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
	s.publish(Event{Kind: EvSubagentTurnStart, SubagentID: s.ID})
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

	disableThinking := isLlamaCPP(s.Adapter)
	var stripper *thinkStripper
	if disableThinking {
		stripper = &thinkStripper{}
	}

	for ev := range stream {
		switch ev.Type {
		case llm.StreamEventText:
			txt := ev.TextDelta
			if disableThinking {
				txt = stripper.Feed(txt)
			}
			if txt != "" {
				text.WriteString(txt)
				s.publish(Event{Kind: EvSubagentText, SubagentID: s.ID, Text: txt})
			}
		case llm.StreamEventToolCall:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
				s.appendTranscript(fmt.Sprintf("tool_call: %s", ev.ToolCall.Name))
				s.publish(Event{Kind: EvSubagentToolCall, SubagentID: s.ID, ToolCall: ev.ToolCall})
			}
		case llm.StreamEventError:
			return text.String(), "", calls, ev.Err
		case llm.StreamEventDone:
			if ev.Usage != nil {
				s.tracker.Add("subagent:"+s.ID, s.Model, *ev.Usage)
				s.publish(Event{Kind: EvSubagentUsage, SubagentID: s.ID, Usage: ev.Usage})
				s.mu.Lock()
				s.lastInputTokens = ev.Usage.InputTokens + ev.Usage.CacheReadTokens + ev.Usage.CacheCreationTokens
				s.mu.Unlock()
			}
			if !disableThinking {
				reasoningContent = ev.ReasoningContent
			}
		}
	}
	if disableThinking {
		txt := stripper.Flush()
		if txt != "" {
			text.WriteString(txt)
			s.publish(Event{Kind: EvSubagentText, SubagentID: s.ID, Text: txt})
		}
	}
	if ctx.Err() != nil {
		return text.String(), "", calls, ctx.Err()
	}
	return text.String(), reasoningContent, calls, nil
}

func isLlamaCPP(a llm.Adapter) bool {
	if a == nil {
		return false
	}
	if fa, ok := a.(*llm.FallbackAdapter); ok {
		for _, entry := range fa.Entries() {
			if isLlamaCPP(entry.Adapter) {
				return true
			}
		}
		return false
	}
	prov := a.Provider()
	return prov == "llamacpp" || prov == "llamacpp-fleet"
}

type thinkStripper struct {
	inThink bool
	buf     string
}

func (ts *thinkStripper) Feed(delta string) string {
	ts.buf += delta
	var output strings.Builder

	for len(ts.buf) > 0 {
		if !ts.inThink {
			idx := strings.Index(ts.buf, "<think>")
			if idx == -1 {
				keep := 0
				for i := 1; i <= 6; i++ {
					if len(ts.buf) >= i && strings.HasSuffix(ts.buf, "<think>"[:i]) {
						keep = i
						break
					}
				}
				output.WriteString(ts.buf[:len(ts.buf)-keep])
				ts.buf = ts.buf[len(ts.buf)-keep:]
				break
			} else {
				output.WriteString(ts.buf[:idx])
				ts.buf = ts.buf[idx+len("<think>"):]
				ts.inThink = true
			}
		} else {
			idx := strings.Index(ts.buf, "</think>")
			if idx == -1 {
				keep := 0
				for i := 1; i <= 7; i++ {
					if len(ts.buf) >= i && strings.HasSuffix(ts.buf, "</think>"[:i]) {
						keep = i
						break
					}
				}
				ts.buf = ts.buf[len(ts.buf)-keep:]
				break
			} else {
				ts.buf = ts.buf[idx+len("</think>"):]
				ts.inThink = false
			}
		}
	}
	return output.String()
}

func (ts *thinkStripper) Flush() string {
	if !ts.inThink {
		return ts.buf
	}
	return ""
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
			s.publish(Event{Kind: EvSubagentInbox, SubagentID: s.ID, Text: msg})
		default:
			return messages
		}
	}
}

// isTransientErr returns true for errors that are likely to succeed on
// retry: deadline exceeded, rate limits, 5xx responses, network errors.
func isTransientErr(err error) bool {
	switch llm.ClassifyError(err) {
	case llm.ErrorClassDeadlineExceeded,
		llm.ErrorClassRateLimit,
		llm.ErrorClassServer,
		llm.ErrorClassNetwork:
		return true
	default:
		return false
	}
}

func (s *Subagent) systemPrompt() string {
	s.mu.Lock()
	caps := s.capabilities
	s.mu.Unlock()

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "unknown"
	}
	cwdBlock := "\n\n<current_directory>\n" + cwd + "\n\nThis is the workspace root / current working directory. You must restrict all search, inspection, and execution to this path. Do not venture outside this directory unless explicitly required by your objective.\n</current_directory>"

	memoriesBlock := ""
	if s.memBlock != "" {
		memoriesBlock = "\n\n" + s.memBlock
	}

	repoMapBlock := ""
	if s.repoMap != "" {
		repoMapBlock = "\n\n<repo_map>\n" + s.repoMap + "\n\nUse this map BEFORE calling grep/glob/read_file. It tells you which files exist and what they contain — use it to plan which files to read, then read them with read_file. The map is intentionally compact; if a file you need isn't listed, fall back to glob/grep.\n</repo_map>"
	}

	roleAddendum := ""
	if s.Task.RoleSystemAddendum != "" {
		roleAddendum = "\n\n<persona>\n" + strings.TrimSpace(s.Task.RoleSystemAddendum) + "\n</persona>"
	}

	capsBlock := buildSubagentCapsBlock(caps)

	editingPolicy := `
<editing_policy>
To optimize token generation speeds, you MUST avoid writing or overwriting entire files.
- To create a new file, use apply_diff with "format": "whole".
- For any edits to existing files, you MUST use apply_diff with search_replace format (SEARCH/REPLACE blocks), edit_file, or multi_edit. Do NOT use whole-file replacement. Keep edits as minimal as possible to avoid slow decoding.
- **CONTROL FILE SIZES & MODULARITY:** Keep files small and modular (prefer under 150-200 lines). If your edits cause a file to exceed this size, you MUST refactor it, splitting and extracting functions/logic into separate files rather than letting one file grow large.
</editing_policy>`

	if isLlamaCPP(s.Adapter) {
		editingPolicy = `
<editing_policy>
You are running under local model constraints (highly sensitive to generation length). You MUST:
- Avoid rewriting entire files or executing long heredocs / cat writes.
- For all edits, use apply_diff with extremely minimal SEARCH/REPLACE blocks.
- Keep each SEARCH block under 5 lines and the replacement under 5 lines.
- Split larger changes into multiple incremental tool calls.
- Never output more than 20 lines of code in a single turn.
- **CONTROL FILE SIZES & MODULARITY:** Code files MUST be kept small and modular (prefer under 100-150 lines). If a file exceeds this size, refactor and extract utilities/functions into separate files.
</editing_policy>`
	}

	toolCallingFormat := ""
	if isLlamaCPP(s.Adapter) {
		toolCallingFormat = `
<tool_calling_format>
To call a tool, you MUST write the tool call on its own line using the Compact DSL format. Do not add any extra text or comments on that line.
Format:
@call:tool_name{"parameter1":"value1","parameter2":42}

Examples:
- To grep for a term:
@call:grep{"pattern":"func NewOpenAI","path":"/home/code/repos/ageni"}

- To read a file:
@call:read_file{"AbsolutePath":"/home/code/repos/ageni/main.go","StartLine":1,"EndLine":50}
</tool_calling_format>`
	}

	if s.Task.ModelTier == "haiku" || s.Task.ModelTier == "fast" || s.Task.ModelTier == "tiny" {
		return `<role>You are a sub-agent in the ageni harness. You execute one focused task delegated by a master agent and return a structured result.</role>` + cwdBlock + roleAddendum + memoriesBlock + capsBlock + `

<rules>
- Stay strictly within the task boundaries you were given.
- Default all operations to the workspace directory shown in <current_directory> and remain within it. Avoid filesystem escaping or querying parent/odd paths unless explicitly required by the objective.
- Use only the tools listed in <allowed_tools>; do not request others.
- Final response: produce exactly one assistant turn that contains a <result>...</result> block matching the requested output_format, followed by a <reasoning>...</reasoning> block summarizing what you did. No tool calls in the final turn.
</rules>` + editingPolicy + toolCallingFormat
	}

	// XML-tagged for Claude (no-op for OpenAI but harmless).
	return `<role>You are a sub-agent in the ageni harness. You execute one focused task delegated by a master agent and return a structured result.</role>` + cwdBlock + roleAddendum + memoriesBlock + repoMapBlock + capsBlock + `

<rules>
- Stay strictly within the task boundaries you were given.
- Default all operations to the workspace directory shown in <current_directory> and remain within it. Avoid filesystem escaping or querying parent/odd paths unless explicitly required by the objective.
- Use only the tools listed in <allowed_tools>; do not request others.
- Respect the tool-call budget. If you cannot complete within budget, return what you have plus a clear blocker description.
- Final response: produce exactly one assistant turn that contains a <result>...</result> block matching the requested output_format, followed by a <reasoning>...</reasoning> block summarizing what you did. No tool calls in the final turn.
- Do not invent file paths, function names, or APIs. If you don't know, say so.
- If the master named a specific skill in <use_skill>, call read_skill on it first and apply its procedures.
</rules>` + editingPolicy + toolCallingFormat + `

<output_discipline>
- Keep the final <result> compact and technical. Prefer terse fragments over narrative prose.
- Skip step-by-step retelling unless the requested output_format explicitly requires it.
- Good: "Updated auth/token.go:42-67. Added nil guard. Reran go test ./auth."
- Bad: "I updated the auth token logic by first inspecting the file, then adding a guard, and finally rerunning tests to make sure everything worked."
</output_discipline>

<self_healing>
When a tool returns an error, do not stop — diagnose and recover autonomously:
- Bad argument / invalid JSON: fix the argument and retry.
- File not found: check if the path exists with list_dir or glob, then use the correct path.
- Permission or transient error: retry up to 2 more times before including it as a blocker in your <result>.
- Unknown tool: use the closest available alternative from <allowed_tools>.
- Missing binary / "not found" error (e.g. "adb not found", "ffmpeg: command not found"): immediately call todo_write to create a task for installing the missing tool (title: "Install <tool>", include the recommended install command in the body), then include the missing dependency as a blocker in your <result> so the master knows to resolve it before retrying.
Never ask the master or the user for help with recoverable errors — handle them yourself.
</self_healing>`
}

func (s *Subagent) userPrompt() string {
	var sb strings.Builder
	var defs []llm.ToolDef
	if s.tools != nil {
		defs = s.tools.Definitions()
	}
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
	if hints := buildTaskExecutionHints(s.Task, defs); hints != "" {
		sb.WriteString(hints)
	}
	remaining := subagentContextBudgetTokens
	var compressed []string
	writeSection := func(section string, used int, note string) {
		if section == "" {
			if note != "" {
				compressed = append(compressed, note)
			}
			return
		}
		sb.WriteString(section)
		remaining -= used
		if note != "" {
			compressed = append(compressed, note)
		}
	}
	section, used, note := buildSubagentListSection("repo_facts", s.Task.RepoFacts, remaining, "")
	writeSection(section, used, note)
	section, used, note = buildSubagentListSection("prior_findings", s.Task.PriorFindings, remaining, "")
	writeSection(section, used, note)
	section, used, note = buildSubagentListSection("do_not_revisit", s.Task.DoNotRevisit, remaining, "(other workers are handling these — stay clear)\n")
	writeSection(section, used, note)
	section, used, note = buildSubagentTextSection("context", s.Task.Context, remaining)
	writeSection(section, used, note)
	if len(compressed) > 0 {
		sb.WriteString("<context_budget_notice>" + strings.Join(compressed, "; ") + "</context_budget_notice>\n")
	}
	sb.WriteString("</task>\n\nBegin.")
	return sb.String()
}

func buildTaskExecutionHints(task SubagentTask, defs []llm.ToolDef) string {
	if len(defs) == 0 || !taskLikelyNeedsMutation(task) {
		return ""
	}
	hints := make([]string, 0, 4)
	if requiresInspectionFirst(task, defs) {
		hints = append(hints, "Inspect current code with a read/search tool before making edits.")
	}
	if hasToolDefNamed(defs, "apply_diff") {
		hints = append(hints, "Prefer apply_diff for multi-line or multi-block edits; reserve edit_file for one exact replacement.")
	}
	if hasToolDefNamed(defs, "transactional_edit") {
		hints = append(hints, "Use transactional_edit for coordinated multi-file changes when validate_command can protect against partial breakage.")
	}
	if hasToolDefNamed(defs, "run_tests") || hasToolDefNamed(defs, "run_bash") {
		hints = append(hints, "After edits, rerun the relevant verification tool before finalizing.")
	}
	if len(hints) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<execution_hints>\n")
	for _, hint := range hints {
		sb.WriteString("- ")
		sb.WriteString(hint)
		sb.WriteByte('\n')
	}
	sb.WriteString("</execution_hints>\n")
	return sb.String()
}

func hasToolDefNamed(defs []llm.ToolDef, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

func buildSubagentListSection(tag string, items []string, budget int, trailing string) (string, int, string) {
	if len(items) == 0 || budget <= 0 {
		if len(items) == 0 {
			return "", 0, ""
		}
		return "", 0, tag + " omitted to fit worker context budget"
	}
	open := "<" + tag + ">\n"
	close := "</" + tag + ">\n"
	closeCost := models.EstimateTokens(close)
	var sb strings.Builder
	sb.WriteString(open)
	used := models.EstimateTokens(open)
	included := 0
	omitted := 0
	for _, item := range items {
		item = subagentClip(item, subagentContextItemMaxChars)
		line := "- " + item + "\n"
		lineCost := models.EstimateTokens(line)
		if included >= subagentContextMaxItems || used+lineCost+closeCost > budget {
			omitted++
			continue
		}
		sb.WriteString(line)
		used += lineCost
		included++
	}
	if trailing != "" {
		trailingCost := models.EstimateTokens(trailing)
		if used+trailingCost+closeCost <= budget {
			sb.WriteString(trailing)
			used += trailingCost
		}
	}
	if included == 0 {
		return "", 0, fmt.Sprintf("%s omitted to fit worker context budget", tag)
	}
	if omitted > 0 {
		noteLine := fmt.Sprintf("- … (%d more omitted to fit worker context budget)\n", omitted)
		noteCost := models.EstimateTokens(noteLine)
		if used+noteCost+closeCost <= budget {
			sb.WriteString(noteLine)
			used += noteCost
		}
	}
	sb.WriteString(close)
	used += closeCost
	note := ""
	if omitted > 0 {
		note = fmt.Sprintf("%s compressed (%d omitted)", tag, omitted)
	}
	return sb.String(), used, note
}

func buildSubagentTextSection(tag, text string, budget int) (string, int, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", 0, ""
	}
	if budget <= 0 {
		return "", 0, tag + " omitted to fit worker context budget"
	}
	open := "<" + tag + ">"
	close := "</" + tag + ">\n"
	overhead := models.EstimateTokens(open) + models.EstimateTokens(close)
	if budget <= overhead {
		return "", 0, tag + " omitted to fit worker context budget"
	}
	maxChars := (budget - overhead) * 4
	note := ""
	if maxChars < len(text) {
		text = subagentClip(text, maxChars)
		note = tag + " truncated"
	}
	rendered := open + text + close
	return rendered, models.EstimateTokens(rendered), note
}

func subagentClip(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// buildSubagentCapsBlock produces the <model_capabilities> XML block injected
// into the subagent's system prompt so it knows what tools it can call based
// on its model's native capabilities.
func buildSubagentCapsBlock(caps []string) string {
	if len(caps) == 0 {
		return ""
	}
	hasIn := func(c string) bool {
		for _, cap := range caps {
			if cap == c {
				return true
			}
		}
		return false
	}
	var sb strings.Builder
	sb.WriteString("\n\n<model_capabilities>")
	sb.WriteString("\nYour model's capabilities in this session:")
	if hasIn("vision") {
		sb.WriteString("\n- vision: you CAN process images. When the task involves an image file, call view_image with the file path.")
	} else {
		sb.WriteString("\n- vision: NOT available on your model. Do not call view_image. Report to the master if image analysis is needed.")
	}
	if hasIn("reasoning") {
		sb.WriteString("\n- reasoning: your model supports extended chain-of-thought. Use it for complex multi-step problems.")
	}
	for _, c := range caps {
		if c != "vision" && c != "reasoning" {
			sb.WriteString("\n- " + c)
		}
	}
	sb.WriteString("\n</model_capabilities>")
	return sb.String()
}

// trimSubagentHistory trims the middle portion of the messages list, preserving the first message
// (initial task setup) and the last N complete assistant turns.
func trimSubagentHistory(messages []llm.Message, keepLastN int) []llm.Message {
	if len(messages) <= 1 {
		return messages
	}

	assistantIndices := []int{}
	for i := len(messages) - 1; i >= 1; i-- {
		if messages[i].Role == llm.RoleAssistant {
			assistantIndices = append(assistantIndices, i)
		}
	}

	if len(assistantIndices) <= keepLastN {
		return messages
	}

	keepFrom := assistantIndices[keepLastN-1]

	notice := llm.Message{
		Role: llm.RoleUser,
		Text: fmt.Sprintf("[Note: %d oldest message(s) removed to fit local model context window]", keepFrom-1),
	}
	trimmed := make([]llm.Message, 0, len(messages)-keepFrom+2)
	trimmed = append(trimmed, messages[0])
	trimmed = append(trimmed, notice)
	trimmed = append(trimmed, messages[keepFrom:]...)
	return trimmed
}

