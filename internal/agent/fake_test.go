package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/models"
	"github.com/bouwerp/ageni/internal/tools"
)

// fakeAdapter scripts a sequence of stream events per call.
type fakeAdapter struct {
	scripts [][]llm.StreamEvent
	calls   int
	reqs    []llm.Request
}

func (f *fakeAdapter) Provider() string { return "fake" }
func (f *fakeAdapter) Stream(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	idx := f.calls
	f.calls++
	f.reqs = append(f.reqs, req)
	if idx >= len(f.scripts) {
		idx = len(f.scripts) - 1
	}
	ch := make(chan llm.StreamEvent, len(f.scripts[idx])+1)
	for _, e := range f.scripts[idx] {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func TestSubagentRunsToolThenFinalText(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t1", Name: "list_dir", Arguments: json.RawMessage(`{"path":"."}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 100, OutputTokens: 20}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done</result><reasoning>listed the dir</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 120, OutputTokens: 30}},
			},
		},
	}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	reg.Register(tools.ListDir{})

	task := SubagentTask{
		Objective:       "list working directory",
		OutputFormat:    "<result>summary</result>",
		AllowedTools:    []string{"list_dir"},
		BudgetToolCalls: 5,
	}
	sub := NewSubagent("s1", task, adapter, "fake-model", reg, bus, tracker, "", "", "", nil, "test-corr")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub.Run(ctx)

	if got := sub.Status(); got != StatusDone {
		t.Fatalf("status=%s, want done", got)
	}
	if got := sub.FinalText(); !strings.Contains(got, "<result>") {
		t.Fatalf("final text missing result block: %q", got)
	}
	snap := tracker.Snapshot()
	if snap.Total.InputTokens != 220 || snap.Total.OutputTokens != 50 {
		t.Fatalf("token tracking wrong: %+v", snap.Total)
	}
}

func TestSubagentRespectsBudget(t *testing.T) {
	// Adapter that keeps calling list_dir forever — should hit budget.
	loop := []llm.StreamEvent{
		{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
			ID: "t", Name: "list_dir", Arguments: json.RawMessage(`{"path":"."}`),
		}},
		{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}},
	}
	scripts := make([][]llm.StreamEvent, 10)
	for i := range scripts {
		scripts[i] = loop
	}
	adapter := &fakeAdapter{scripts: scripts}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	reg.Register(tools.ListDir{})

	task := SubagentTask{
		Objective:       "loop forever",
		OutputFormat:    "<result/>",
		BudgetToolCalls: 3,
	}
	sub := NewSubagent("s1", task, adapter, "m", reg, bus, tracker, "", "", "", nil, "test-corr")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub.Run(ctx)

	if got := sub.Status(); got != StatusError {
		t.Fatalf("expected error status on budget exhaustion, got %s", got)
	}
}

func TestSpawnRequiresContract(t *testing.T) {
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	factory := func(tier string, _ []string) (llm.Adapter, string) {
		return &fakeAdapter{scripts: [][]llm.StreamEvent{{{Type: llm.StreamEventDone}}}}, "m"
	}
	mgr := NewManager(context.Background(), bus, reg, tracker, factory, 4)

	if _, err := mgr.Spawn(context.Background(), SubagentTask{}); err == nil {
		t.Fatal("expected error for empty task, got nil")
	}
	if _, err := mgr.Spawn(context.Background(), SubagentTask{Objective: "x"}); err == nil {
		t.Fatal("expected error for missing output_format, got nil")
	}
	if _, err := mgr.Spawn(context.Background(), SubagentTask{Objective: "x", OutputFormat: "y"}); err != nil {
		t.Fatalf("expected ok spawn, got %v", err)
	}
}

func TestMasterCallsToolThenAnswers(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventText, TextDelta: "let me check"},
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t1", Name: "list_dir", Arguments: json.RawMessage(`{"path":"."}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "here is the listing"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 15, OutputTokens: 8}},
			},
		},
	}
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	reg.Register(tools.ListDir{})
	factory := func(tier string, _ []string) (llm.Adapter, string) { return adapter, "m" }
	mgr := NewManager(context.Background(), bus, reg, tracker, factory, 4)
	master := NewMaster(adapter, "m", reg, bus, tracker, mgr)

	inbox := make(chan Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go master.Run(ctx, inbox)

	// Subscribe before sending so we see the events.
	sub := bus.Subscribe(64)
	inbox <- Event{Kind: EvUserMessage, Text: "what's in the dir?"}

	deadline := time.After(2 * time.Second)
	saw := map[EventKind]bool{}
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout; saw=%v", saw)
		case ev := <-sub:
			saw[ev.Kind] = true
			if saw[EvMasterTurnDone] {
				if !saw[EvMasterToolCall] || !saw[EvMasterToolDone] {
					t.Fatalf("missing tool events; saw=%v", saw)
				}
				return
			}
		}
	}
}

func TestMasterActiveContextIsEphemeral(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventText, TextDelta: "done"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
	}
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	mgr := NewManager(context.Background(), bus, reg, tracker, func(string, []string) (llm.Adapter, string) {
		return adapter, "m"
	}, 1)
	master := NewMaster(adapter, "m", reg, bus, tracker, mgr)
	master.messages = []llm.Message{{Role: llm.RoleUser, Text: "start"}}
	master.pendingEvs = []Event{{Kind: EvSubagentRetry, SubagentID: "s1", Text: "retrying tool"}}

	master.takeTurns(context.Background())

	if len(adapter.reqs) != 1 {
		t.Fatalf("adapter reqs = %d, want 1", len(adapter.reqs))
	}
	reqMsgs := adapter.reqs[0].Messages
	if len(reqMsgs) != 2 {
		t.Fatalf("request messages = %d, want 2", len(reqMsgs))
	}
	last := reqMsgs[len(reqMsgs)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text, "<active_context>") {
		t.Fatalf("last request message = %+v, want ephemeral active context", last)
	}
	if got := master.pendingEvs; len(got) != 0 {
		t.Fatalf("pending events not cleared: %+v", got)
	}
	for _, msg := range master.messages {
		if strings.Contains(msg.Text, "<active_context>") {
			t.Fatalf("durable history contains active context: %+v", master.messages)
		}
	}
	if len(master.messages) != 2 {
		t.Fatalf("durable message count = %d, want 2", len(master.messages))
	}
}

func TestMasterSystemPromptRespectsBudget(t *testing.T) {
	master := &Master{}
	master.SetAgentsMD(strings.Repeat("project rule\n", 500))
	master.SetSkillCatalog(strings.Repeat("skill-entry\n", 12_000))
	master.SetRoleCatalog(strings.Repeat("role-entry\n", 12_000))
	master.SetRepoMap(strings.Repeat("path/file.go: symbol\n", 12_000))

	prompt := master.systemPrompt()

	if got := models.EstimateTokens(prompt); got > systemPromptBudgetTokens {
		t.Fatalf("system prompt estimated at %d tokens, want <= %d", got, systemPromptBudgetTokens)
	}
	if !strings.Contains(prompt, "<prompt_budget_notice>") {
		t.Fatalf("expected prompt budget notice, got prompt without one")
	}
	if !strings.Contains(prompt, "<project_instructions source=\"AGENTS.md\">") {
		t.Fatalf("expected higher-priority AGENTS block to remain")
	}
	if strings.Contains(prompt, "<repo_map>") {
		t.Fatalf("expected lower-priority repo_map block to be omitted under budget pressure")
	}
}

func TestMasterSystemPromptKeepsSmallOptionalBlocks(t *testing.T) {
	master := &Master{}
	master.SetSkillCatalog("skill-a")
	master.SetRoleCatalog("role-a")
	master.SetRepoMap("path/file.go: func A")
	master.SetAgentsMD("Follow nested instructions.")

	prompt := master.systemPrompt()

	if strings.Contains(prompt, "<prompt_budget_notice>") {
		t.Fatalf("did not expect prompt budget notice for small prompt")
	}
	for _, want := range []string{
		"<available_skills>",
		"<available_roles>",
		"<repo_map>",
		"<project_instructions source=\"AGENTS.md\">",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q", want)
		}
	}
}

func TestMasterCompactionProducesStructuredContext(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventText, TextDelta: `<compacted_context>
<summary>Earlier work established the baseline.</summary>
<decisions>
- keep durable state structured
</decisions>
<completed>
- reviewed the oldest exchange
</completed>
<pending>
- continue with newer exchanges
</pending>
<artifacts>
- internal/agent/master.go
</artifacts>
</compacted_context>`},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 12, OutputTokens: 8}},
			},
		},
	}
	bus := NewBus()
	tracker := llm.NewTracker()
	master := NewMaster(adapter, "m", tools.NewRegistry(), bus, tracker, nil)
	master.messages = []llm.Message{
		{Role: llm.RoleUser, Text: "u1"},
		{Role: llm.RoleAssistant, Text: "a1"},
		{Role: llm.RoleUser, Text: "u2"},
		{Role: llm.RoleAssistant, Text: "a2"},
		{Role: llm.RoleUser, Text: "u3"},
		{Role: llm.RoleAssistant, Text: "a3"},
		{Role: llm.RoleUser, Text: "u4"},
		{Role: llm.RoleAssistant, Text: "a4"},
	}

	master.compactHistory(context.Background())

	if len(master.messages) != 7 {
		t.Fatalf("message count after compaction = %d, want 7", len(master.messages))
	}
	if !strings.HasPrefix(master.messages[0].Text, "<compacted_context>") {
		t.Fatalf("first message = %q, want structured compacted context", master.messages[0].Text)
	}
	if master.messages[1].Role != llm.RoleUser || master.messages[1].Text != "u2" {
		t.Fatalf("expected compaction to preserve whole exchange boundary, got %+v", master.messages[1])
	}
}

func TestMasterCompactionNormalizesPlainSummary(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventText, TextDelta: "plain prose summary"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 12, OutputTokens: 8}},
			},
		},
	}
	bus := NewBus()
	tracker := llm.NewTracker()
	master := NewMaster(adapter, "m", tools.NewRegistry(), bus, tracker, nil)
	master.messages = []llm.Message{
		{Role: llm.RoleUser, Text: "u1"},
		{Role: llm.RoleAssistant, Text: "a1"},
		{Role: llm.RoleUser, Text: "u2"},
		{Role: llm.RoleAssistant, Text: "a2"},
		{Role: llm.RoleUser, Text: "u3"},
		{Role: llm.RoleAssistant, Text: "a3"},
		{Role: llm.RoleUser, Text: "u4"},
		{Role: llm.RoleAssistant, Text: "a4"},
	}

	master.compactHistory(context.Background())

	if !strings.HasPrefix(master.messages[0].Text, "<compacted_context>") {
		t.Fatalf("expected normalized structured context, got %q", master.messages[0].Text)
	}
	if !strings.Contains(master.messages[0].Text, "<summary>plain prose summary</summary>") {
		t.Fatalf("expected raw summary to be preserved in normalized block, got %q", master.messages[0].Text)
	}
}

func TestSubagentPauseResume(t *testing.T) {
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	sub := NewSubagent("s1", SubagentTask{Objective: "x", OutputFormat: "y"}, &fakeAdapter{scripts: [][]llm.StreamEvent{{{Type: llm.StreamEventDone}}}}, "m", reg, bus, tracker, "", "", "", nil, "test-corr")
	sub.setStatus(StatusRunning)

	subEvents := bus.Subscribe(8)
	if !sub.Pause() {
		t.Fatal("Pause() = false, want true")
	}
	if got := sub.Status(); got != StatusPaused {
		t.Fatalf("status after pause = %s, want %s", got, StatusPaused)
	}
	if !sub.Resume() {
		t.Fatal("Resume() = false, want true")
	}
	if got := sub.Status(); got != StatusRunning {
		t.Fatalf("status after resume = %s, want %s", got, StatusRunning)
	}
	seen := map[EventKind]bool{}
	for i := 0; i < 2; i++ {
		seen[(<-subEvents).Kind] = true
	}
	if !seen[EvSubagentPaused] || !seen[EvSubagentResumed] {
		t.Fatalf("pause/resume events missing: %+v", seen)
	}
}

func TestMasterPauseResume(t *testing.T) {
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	mgr := NewManager(context.Background(), bus, reg, tracker, func(string, []string) (llm.Adapter, string) { return nil, "" }, 1)
	master := NewMaster(nil, "m", reg, bus, tracker, mgr)

	sub := bus.Subscribe(8)
	if !master.Pause() {
		t.Fatal("Pause() = false, want true")
	}
	if !master.Paused() {
		t.Fatal("master should be paused")
	}
	if !master.Resume() {
		t.Fatal("Resume() = false, want true")
	}
	if master.Paused() {
		t.Fatal("master should not be paused")
	}
	seen := map[EventKind]bool{}
	for i := 0; i < 2; i++ {
		seen[(<-sub).Kind] = true
	}
	if !seen[EvMasterPaused] || !seen[EvMasterResumed] {
		t.Fatalf("pause/resume events missing: %+v", seen)
	}
}
