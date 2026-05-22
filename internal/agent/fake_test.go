package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/memory"
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

type scriptedTool struct {
	name    string
	outputs []string
	calls   int
}

func (t *scriptedTool) Name() string        { return t.name }
func (t *scriptedTool) Description() string { return "scripted test tool" }
func (t *scriptedTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}
func (t *scriptedTool) Call(context.Context, json.RawMessage) (string, error) {
	idx := t.calls
	t.calls++
	if idx >= len(t.outputs) {
		idx = len(t.outputs) - 1
	}
	return t.outputs[idx], nil
}

type scriptedErrorTool struct {
	name   string
	errors []string
	calls  int
}

func (t *scriptedErrorTool) Name() string        { return t.name }
func (t *scriptedErrorTool) Description() string { return "scripted test error tool" }
func (t *scriptedErrorTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}
func (t *scriptedErrorTool) Call(context.Context, json.RawMessage) (string, error) {
	idx := t.calls
	t.calls++
	if idx >= len(t.errors) {
		idx = len(t.errors) - 1
	}
	return "", errors.New(t.errors[idx])
}

func toolNames(defs []llm.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
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

func TestSubagentRemindsOnUnresolvedLintBeforeFinalText(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t1", Name: "edit_file", Arguments: json.RawMessage(`{"path":"foo.go","old_string":"a","new_string":"b"}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done too early</result><reasoning>finished</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t2", Name: "edit_file", Arguments: json.RawMessage(`{"path":"foo.go","old_string":"b","new_string":"b"}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done</result><reasoning>fixed lint and finished</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
	}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	tool := &scriptedTool{
		name: "edit_file",
		outputs: []string{
			"replaced 1 occurrence in foo.go\n[lint] gofmt: file needs reformat (run `gofmt -w foo.go`):\n--- before",
			"replaced 1 occurrence in foo.go\n[lint] gofmt: ok",
		},
	}
	reg.Register(tool)

	task := SubagentTask{
		Objective:       "edit foo.go",
		OutputFormat:    "<result>summary</result>",
		AllowedTools:    []string{"edit_file"},
		BudgetToolCalls: 5,
	}
	sub := NewSubagent("s1", task, adapter, "fake-model", reg, bus, tracker, "", "", "", nil, "test-corr")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub.Run(ctx)

	if got := sub.Status(); got != StatusDone {
		t.Fatalf("status=%s, want done", got)
	}
	if tool.calls != 2 {
		t.Fatalf("tool calls=%d, want 2", tool.calls)
	}
	if len(adapter.reqs) != 4 {
		t.Fatalf("adapter reqs=%d, want 4", len(adapter.reqs))
	}
	req := adapter.reqs[2]
	last := req.Messages[len(req.Messages)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text, "verification-related tool results") {
		t.Fatalf("expected lint reminder in request, got %+v", last)
	}
	if got := sub.FinalText(); !strings.Contains(got, "<result>done</result>") {
		t.Fatalf("final text=%q, want final result after lint fix", got)
	}
}

func TestSubagentRemindsOnFailedTestsBeforeFinalText(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t1", Name: "run_tests", Arguments: json.RawMessage(`{}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done too early</result><reasoning>finished</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t2", Name: "run_tests", Arguments: json.RawMessage(`{}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done</result><reasoning>tests now pass</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
	}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	tool := &scriptedTool{
		name: "run_tests",
		outputs: []string{
			"[exit 1]\nFAIL\tgithub.com/example/project\t0.123s",
			"[exit 0]\nok\tgithub.com/example/project\t0.123s",
		},
	}
	reg.Register(tool)

	task := SubagentTask{
		Objective:       "fix and verify tests",
		OutputFormat:    "<result>summary</result>",
		AllowedTools:    []string{"run_tests"},
		BudgetToolCalls: 5,
	}
	sub := NewSubagent("s1", task, adapter, "fake-model", reg, bus, tracker, "", "", "", nil, "test-corr")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub.Run(ctx)

	if got := sub.Status(); got != StatusDone {
		t.Fatalf("status=%s, want done", got)
	}
	if tool.calls != 2 {
		t.Fatalf("tool calls=%d, want 2", tool.calls)
	}
	if len(adapter.reqs) != 4 {
		t.Fatalf("adapter reqs=%d, want 4", len(adapter.reqs))
	}
	req := adapter.reqs[2]
	last := req.Messages[len(req.Messages)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text, "verification-related tool results") {
		t.Fatalf("expected test reminder in request, got %+v", last)
	}
	if got := sub.FinalText(); !strings.Contains(got, "<result>done</result>") {
		t.Fatalf("final text=%q, want final result after test fix", got)
	}
}

func TestSubagentRemindsToSwitchEditBackendsBeforeFinalText(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t1", Name: "edit_file", Arguments: json.RawMessage(`{"path":"foo.go","old_string":"before","new_string":"after"}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done too early</result><reasoning>finished</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t2", Name: "apply_diff", Arguments: json.RawMessage(`{"path":"foo.go","content":"<<<<<<< SEARCH\nbefore\n=======\nafter\n>>>>>>> REPLACE"}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done</result><reasoning>switched edit backends and finished</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
	}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	editTool := &scriptedErrorTool{
		name: "edit_file",
		errors: []string{
			"old_string not found in foo.go; if this is a multi-line or approximate edit, prefer apply_diff with SEARCH/REPLACE blocks because it returns closest candidate regions when SEARCH misses",
		},
	}
	applyDiffTool := &scriptedTool{
		name:    "apply_diff",
		outputs: []string{"applied 1 block(s) to foo.go (search_replace)"},
	}
	reg.Register(editTool)
	reg.Register(applyDiffTool)

	task := SubagentTask{
		Objective:       "edit foo.go safely",
		OutputFormat:    "<result>summary</result>",
		AllowedTools:    []string{"edit_file", "apply_diff"},
		BudgetToolCalls: 5,
	}
	sub := NewSubagent("s1", task, adapter, "fake-model", reg, bus, tracker, "", "", "", nil, "test-corr")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub.Run(ctx)

	if got := sub.Status(); got != StatusDone {
		t.Fatalf("status=%s, want done", got)
	}
	if editTool.calls != 1 {
		t.Fatalf("edit_file calls=%d, want 1", editTool.calls)
	}
	if applyDiffTool.calls != 1 {
		t.Fatalf("apply_diff calls=%d, want 1", applyDiffTool.calls)
	}
	if len(adapter.reqs) != 4 {
		t.Fatalf("adapter reqs=%d, want 4", len(adapter.reqs))
	}
	req := adapter.reqs[2]
	last := req.Messages[len(req.Messages)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text, "exact-match edit tool failed") || !strings.Contains(last.Text, "Switch to apply_diff") {
		t.Fatalf("expected edit recovery reminder in request, got %+v", last)
	}
	if got := sub.FinalText(); !strings.Contains(got, "<result>done</result>") {
		t.Fatalf("final text=%q, want final result after backend switch", got)
	}
}

func TestSubagentRequiresInspectionBeforeMutationOnEditTasks(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t1", Name: "read_file", Arguments: json.RawMessage(`{"path":"foo.go"}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t2", Name: "apply_diff", Arguments: json.RawMessage(`{"path":"foo.go","content":"<<<<<<< SEARCH\nbefore\n=======\nafter\n>>>>>>> REPLACE"}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done</result><reasoning>inspected first, then edited</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
	}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	reg.Register(&scriptedTool{name: "read_file", outputs: []string{"[foo.go lines 1-1 of 1]\nbefore\n"}})
	reg.Register(&scriptedTool{name: "apply_diff", outputs: []string{"applied 1 block(s) to foo.go (search_replace)"}})

	task := SubagentTask{
		Objective:       "refactor foo.go safely",
		OutputFormat:    "<result>summary</result>",
		AllowedTools:    []string{"read_file", "apply_diff"},
		BudgetToolCalls: 5,
	}
	sub := NewSubagent("s1", task, adapter, "fake-model", reg, bus, tracker, "", "", "", nil, "test-corr")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub.Run(ctx)

	if got := sub.Status(); got != StatusDone {
		t.Fatalf("status=%s, want done", got)
	}
	if got, want := toolNames(adapter.reqs[0].Tools), []string{"read_file"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("first-turn tools=%v, want %v", got, want)
	}
	secondTurnTools := toolNames(adapter.reqs[1].Tools)
	foundApplyDiff := false
	for _, name := range secondTurnTools {
		if name == "apply_diff" {
			foundApplyDiff = true
			break
		}
	}
	if !foundApplyDiff {
		t.Fatalf("second-turn tools=%v, want apply_diff after inspection", secondTurnTools)
	}
}

func TestSubagentRemindsToInspectBeforeFinalizingEditTask(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done too early</result><reasoning>finished</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t1", Name: "read_file", Arguments: json.RawMessage(`{"path":"foo.go"}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>blocked after inspection</result><reasoning>need one more edit step later</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
	}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	reg.Register(&scriptedTool{name: "read_file", outputs: []string{"[foo.go lines 1-1 of 1]\nbefore\n"}})
	reg.Register(&scriptedTool{name: "apply_diff", outputs: []string{"applied 1 block(s) to foo.go (search_replace)"}})

	task := SubagentTask{
		Objective:       "update foo.go",
		OutputFormat:    "<result>summary</result>",
		AllowedTools:    []string{"read_file", "apply_diff"},
		BudgetToolCalls: 5,
	}
	sub := NewSubagent("s1", task, adapter, "fake-model", reg, bus, tracker, "", "", "", nil, "test-corr")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub.Run(ctx)

	if got := sub.Status(); got != StatusDone {
		t.Fatalf("status=%s, want done", got)
	}
	if len(adapter.reqs) != 3 {
		t.Fatalf("adapter reqs=%d, want 3", len(adapter.reqs))
	}
	last := adapter.reqs[1].Messages[len(adapter.reqs[1].Messages)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text, "Inspect the current code before mutating files") {
		t.Fatalf("expected inspection reminder, got %+v", last)
	}
}

func TestSubagentRemindsOnFailedRunBashTestsBeforeFinalText(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t1", Name: "run_bash", Arguments: json.RawMessage(`{"command":"go test ./..."}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done too early</result><reasoning>finished</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t2", Name: "run_bash", Arguments: json.RawMessage(`{"command":"go test ./..."}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done</result><reasoning>bash-driven tests now pass</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
	}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	tool := &scriptedTool{
		name: "run_bash",
		outputs: []string{
			"[exit 1]\nFAIL\tgithub.com/example/project\t0.123s",
			"[exit 0]\nok\tgithub.com/example/project\t0.123s",
		},
	}
	reg.Register(tool)

	task := SubagentTask{
		Objective:       "fix and verify bash-driven tests",
		OutputFormat:    "<result>summary</result>",
		AllowedTools:    []string{"run_bash"},
		BudgetToolCalls: 5,
	}
	sub := NewSubagent("s1", task, adapter, "fake-model", reg, bus, tracker, "", "", "", nil, "test-corr")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub.Run(ctx)

	if got := sub.Status(); got != StatusDone {
		t.Fatalf("status=%s, want done", got)
	}
	req := adapter.reqs[2]
	last := req.Messages[len(req.Messages)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text, "verification-related tool results") {
		t.Fatalf("expected verification reminder in request, got %+v", last)
	}
	if got := sub.FinalText(); !strings.Contains(got, "<result>done</result>") {
		t.Fatalf("final text=%q, want final result after bash tests pass", got)
	}
}

func TestSubagentRemindsOnFailedRunBashBuildBeforeFinalText(t *testing.T) {
	adapter := &fakeAdapter{
		scripts: [][]llm.StreamEvent{
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t1", Name: "run_bash", Arguments: json.RawMessage(`{"command":"go build ./..."}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done too early</result><reasoning>finished</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventToolCall, ToolCall: &llm.ToolCall{
					ID: "t2", Name: "run_bash", Arguments: json.RawMessage(`{"command":"go build ./..."}`),
				}},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
			{
				{Type: llm.StreamEventText, TextDelta: "<result>done</result><reasoning>build now passes</reasoning>"},
				{Type: llm.StreamEventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
	}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	tool := &scriptedTool{
		name: "run_bash",
		outputs: []string{
			"[exit 1]\n# github.com/example/project\n./main.go:10:2: undefined: missing",
			"[exit 0]\n",
		},
	}
	reg.Register(tool)

	task := SubagentTask{
		Objective:       "fix and verify build",
		OutputFormat:    "<result>summary</result>",
		AllowedTools:    []string{"run_bash"},
		BudgetToolCalls: 5,
	}
	sub := NewSubagent("s1", task, adapter, "fake-model", reg, bus, tracker, "", "", "", nil, "test-corr")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub.Run(ctx)

	if got := sub.Status(); got != StatusDone {
		t.Fatalf("status=%s, want done", got)
	}
	req := adapter.reqs[2]
	last := req.Messages[len(req.Messages)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text, "[build] [exit 1]") {
		t.Fatalf("expected build verification reminder in request, got %+v", last)
	}
	if got := sub.FinalText(); !strings.Contains(got, "<result>done</result>") {
		t.Fatalf("final text=%q, want final result after build passes", got)
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
	reg.Register(tools.ListDir{})
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
	if _, err := mgr.Spawn(context.Background(), SubagentTask{
		Objective:    "x",
		OutputFormat: "y",
		AllowedTools: []string{"pause_subagent", "list_dir"},
	}); err == nil || !strings.Contains(err.Error(), "unavailable to sub-agents") {
		t.Fatalf("expected unavailable tool error, got %v", err)
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

func TestMasterActiveContextIncludesSupervisionSignals(t *testing.T) {
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	mgr := NewManager(context.Background(), bus, reg, tracker, nil, 1)
	master := NewMaster(&fakeAdapter{}, "m", reg, bus, tracker, mgr)
	master.handleInboxEvent(Event{Kind: EvSubagentSpawn, SubagentID: "s1", SubagentTask: "inspect auth", SubagentModel: "claude-sonnet"})
	master.handleInboxEvent(Event{Kind: EvSubagentPaused, SubagentID: "s1"})
	master.pendingEvs = []Event{
		{Kind: EvTick, Text: "worker s1 stalled"},
		{Kind: EvSubagentRetry, SubagentID: "s1", Text: "retrying tool"},
		{Kind: EvSubagentRetry, SubagentID: "s1", Text: "retrying tool again"},
		{Kind: EvShellOutputLoss, SubagentID: "sh1", Bytes: 512},
	}

	msg := master.buildActiveContext()
	if msg == nil {
		t.Fatal("buildActiveContext() = nil, want active context message")
	}
	if !strings.Contains(msg.Text, "<supervision_summary>") {
		t.Fatalf("active context missing supervision summary block: %s", msg.Text)
	}
	if !strings.Contains(msg.Text, "states: paused=1") {
		t.Fatalf("active context missing compact state counts: %s", msg.Text)
	}
	if !strings.Contains(msg.Text, "s1 [paused") {
		t.Fatalf("active context missing compact worker summary: %s", msg.Text)
	}
	if !strings.Contains(msg.Text, "retry x2") {
		t.Fatalf("active context missing aggregated retry summary: %s", msg.Text)
	}
	if !strings.Contains(msg.Text, "supervision tick: worker s1 stalled") {
		t.Fatalf("active context missing supervision tick delta: %s", msg.Text)
	}
	if !strings.Contains(msg.Text, "shell sh1 output loss: 512 byte(s)") {
		t.Fatalf("active context missing shell loss warning: %s", msg.Text)
	}
	if strings.Contains(msg.Text, "Sub-agents (current state):") || strings.Contains(msg.Text, "New events since your last turn:") {
		t.Fatalf("active context still uses verbose legacy headings: %s", msg.Text)
	}
}

func TestMasterTickDoesNotTriggerTurnForHealthyWorkers(t *testing.T) {
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	mgr := NewManager(context.Background(), bus, reg, tracker, nil, 1)
	mgr.subs["s1"] = &Subagent{ID: "s1", status: StatusRunning}
	master := NewMaster(&fakeAdapter{}, "m", reg, bus, tracker, mgr)

	if got := master.handleInboxEvent(Event{Kind: EvTick}); got {
		t.Fatal("EvTick triggered a master turn for a healthy running worker")
	}
	if got := len(master.pendingEvs); got != 0 {
		t.Fatalf("pending events = %d, want 0", got)
	}
}

func TestMasterTickDoesNotEscalatePendingNonActionableEvents(t *testing.T) {
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	mgr := NewManager(context.Background(), bus, reg, tracker, nil, 1)
	mgr.subs["s1"] = &Subagent{ID: "s1", status: StatusRunning}
	master := NewMaster(&fakeAdapter{}, "m", reg, bus, tracker, mgr)
	master.pendingEvs = []Event{{Kind: EvSubagentRetry, SubagentID: "s1", Text: "retrying tool"}}

	if got := master.handleInboxEvent(Event{Kind: EvTick}); got {
		t.Fatal("EvTick escalated non-actionable pending events into a master turn")
	}
	if got := len(master.pendingEvs); got != 1 {
		t.Fatalf("pending events = %d, want 1", got)
	}
	if master.pendingEvs[0].Kind != EvSubagentRetry {
		t.Fatalf("pending event mutated to %s, want retry event preserved", master.pendingEvs[0].Kind)
	}
}

func TestMasterDoneAndErrorEventsStillTriggerTurns(t *testing.T) {
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	mgr := NewManager(context.Background(), bus, reg, tracker, nil, 1)
	master := NewMaster(&fakeAdapter{}, "m", reg, bus, tracker, mgr)

	if got := master.handleInboxEvent(Event{Kind: EvSubagentDone, SubagentID: "s1", Text: "<result>done</result>"}); !got {
		t.Fatal("EvSubagentDone should trigger a master turn")
	}
	master.pendingEvs = nil
	if got := master.handleInboxEvent(Event{Kind: EvSubagentError, SubagentID: "s1", Err: errors.New("boom")}); !got {
		t.Fatal("EvSubagentError should trigger a master turn")
	}
}

func TestMasterTickTriggersTurnForStalledWorker(t *testing.T) {
	base := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	now := base
	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	mgr := NewManager(context.Background(), bus, reg, tracker, nil, 1)
	mgr.subs["s1"] = &Subagent{ID: "s1", status: StatusRunning}
	master := NewMaster(&fakeAdapter{}, "m", reg, bus, tracker, mgr)
	master.supervisor = NewSupervisorState(func() time.Time { return now })
	master.supervisor.stalledAfter = 30 * time.Second

	master.handleInboxEvent(Event{Kind: EvSubagentSpawn, SubagentID: "s1", SubagentTask: "run tests"})
	now = base.Add(31 * time.Second)

	if got := master.handleInboxEvent(Event{Kind: EvTick}); !got {
		t.Fatal("EvTick should trigger a master turn for a stalled worker")
	}
	if got := len(master.pendingEvs); got != 2 {
		t.Fatalf("pending events = %+v, want spawn plus one synthetic stall tick", master.pendingEvs)
	}
	last := master.pendingEvs[len(master.pendingEvs)-1]
	if last.Kind != EvTick {
		t.Fatalf("last pending event = %+v, want synthetic stall tick", last)
	}
	if !strings.Contains(last.Text, "stalled") {
		t.Fatalf("expected stall reason in tick text, got %+v", last)
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

func TestFitPromptBlocksTruncatesTaggedBlockToFitBudget(t *testing.T) {
	block := promptContextBlock{
		name: "repository map",
		text: "<repo_map>\n" + strings.Repeat("internal/very/long/path/file.go: symbol\n", 400) + "</repo_map>",
	}

	out := fitPromptBlocks(120, []promptContextBlock{block})

	if got := models.EstimateTokens(out); got > 120 {
		t.Fatalf("fitPromptBlocks output estimated at %d tokens, want <= 120", got)
	}
	if !strings.Contains(out, "<repo_map>") || !strings.Contains(out, "</repo_map>") {
		t.Fatalf("expected truncated tagged block to remain well-formed, got %q", out)
	}
	if !strings.Contains(out, "truncated to fit prompt budget") {
		t.Fatalf("expected truncated block marker, got %q", out)
	}
}

func TestMasterAdapterForIterUsesLeadOnIntegrationTurn(t *testing.T) {
	lead := &fakeAdapter{}
	worker := &fakeAdapter{}
	master := &Master{
		adapter:     worker,
		model:       "worker-model",
		leadAdapter: lead,
		leadModel:   "lead-model",
		messages: []llm.Message{
			{Role: llm.RoleUser, Text: "Fix the build"},
			{Role: llm.RoleAssistant, Text: "Checking"},
			{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{Content: "test output"}}},
		},
	}

	adapter, model := master.adapterForIter(1)
	if adapter != lead || model != "lead-model" {
		t.Fatalf("adapterForIter(1) = (%v, %q), want lead adapter", adapter, model)
	}
}

func TestMasterAdapterForIterKeepsWorkerForNonIntegrationTurns(t *testing.T) {
	lead := &fakeAdapter{}
	worker := &fakeAdapter{}
	master := &Master{
		adapter:     worker,
		model:       "worker-model",
		leadAdapter: lead,
		leadModel:   "lead-model",
		messages: []llm.Message{
			{Role: llm.RoleUser, Text: "Fix the build"},
			{Role: llm.RoleAssistant, Text: "Still exploring"},
		},
	}

	adapter, model := master.adapterForIter(1)
	if adapter != worker || model != "worker-model" {
		t.Fatalf("adapterForIter(1) = (%v, %q), want worker adapter", adapter, model)
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

func TestSubagentUserPromptCompressesLargeContext(t *testing.T) {
	task := SubagentTask{
		Objective:    "inspect auth flow",
		OutputFormat: "<result/>",
		RepoFacts: []string{
			strings.Repeat("repo-fact-1 ", 40),
			strings.Repeat("repo-fact-2 ", 40),
			strings.Repeat("repo-fact-3 ", 40),
			strings.Repeat("repo-fact-4 ", 40),
			strings.Repeat("repo-fact-5 ", 40),
			strings.Repeat("repo-fact-6 ", 40),
			strings.Repeat("repo-fact-7 ", 40),
			strings.Repeat("repo-fact-8 ", 40),
			strings.Repeat("repo-fact-9 ", 40),
			strings.Repeat("repo-fact-10 ", 40),
		},
		PriorFindings: []string{
			strings.Repeat("prior-finding ", 50),
			strings.Repeat("prior-finding ", 50),
			strings.Repeat("prior-finding ", 50),
			strings.Repeat("prior-finding ", 50),
			strings.Repeat("prior-finding ", 50),
		},
		DoNotRevisit: []string{
			strings.Repeat("path/area-one ", 40),
			strings.Repeat("path/area-two ", 40),
			strings.Repeat("path/area-three ", 40),
			strings.Repeat("path/area-four ", 40),
			strings.Repeat("path/area-five ", 40),
		},
		Context: strings.Repeat("free-form context ", 600),
	}

	prompt := (&Subagent{Task: task}).userPrompt()

	if !strings.Contains(prompt, "<context_budget_notice>") {
		t.Fatalf("expected context budget notice in prompt")
	}
	if !strings.Contains(prompt, "repo_facts compressed") && !strings.Contains(prompt, "context truncated") {
		t.Fatalf("expected compression note, got %q", prompt)
	}
	if got := models.EstimateTokens(prompt); got > subagentContextBudgetTokens+400 {
		t.Fatalf("compressed worker prompt estimated at %d tokens, want <= %d", got, subagentContextBudgetTokens+400)
	}
}

func TestSubagentUserPromptKeepsSmallContext(t *testing.T) {
	task := SubagentTask{
		Objective:      "inspect auth flow",
		OutputFormat:   "<result/>",
		RepoFacts:      []string{"internal/auth/jwt.go: issues tokens"},
		PriorFindings:  []string{"s3 found middleware in internal/http/auth.go:42"},
		DoNotRevisit:   []string{"internal/http/router.go"},
		Context:        "Focus on token expiry handling.",
		TaskBoundaries: "Do not edit files.",
	}

	prompt := (&Subagent{Task: task}).userPrompt()

	if strings.Contains(prompt, "<context_budget_notice>") {
		t.Fatalf("did not expect budget notice for small context")
	}
	for _, want := range []string{"<repo_facts>", "<prior_findings>", "<do_not_revisit>", "<context>Focus on token expiry handling.</context>"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q", want)
		}
	}
}

func TestSubagentUserPromptAddsTaskConditionedExecutionHints(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	reg.Register(tools.ApplyDiff{})
	reg.Register(tools.RunTests{})

	prompt := (&Subagent{
		Task: SubagentTask{
			Objective:    "refactor auth flow and verify tests",
			OutputFormat: "<result/>",
		},
		tools: reg,
	}).userPrompt()

	for _, want := range []string{
		"<execution_hints>",
		"Inspect current code with a read/search tool before making edits.",
		"Prefer apply_diff for multi-line or multi-block edits",
		"After edits, rerun the relevant verification tool before finalizing.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestSubagentUserPromptSkipsTaskConditionedHintsForReadOnlyTask(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	reg.Register(tools.ListDir{})

	prompt := (&Subagent{
		Task: SubagentTask{
			Objective:      "inspect auth flow",
			OutputFormat:   "<result/>",
			TaskBoundaries: "Do not edit files.",
		},
		tools: reg,
	}).userPrompt()

	if strings.Contains(prompt, "<execution_hints>") {
		t.Fatalf("did not expect execution hints for read-only task, got %q", prompt)
	}
}

func TestSubagentSystemPromptIncludesEditingPolicy(t *testing.T) {
	prompt := (&Subagent{}).systemPrompt()

	for _, want := range []string{
		"<editing_policy>",
		"Prefer edit_file only for one exact replacement",
		"Prefer apply_diff for multi-line or multi-block edits",
		"Prefer transactional_edit for coordinated multi-file changes",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected subagent system prompt to contain %q", want)
		}
	}
}

func TestLatestMemoryQuerySkipsSyntheticUserMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Text: "Investigate auth regressions"},
		{Role: llm.RoleUser, Text: "<active_context>ignore this</active_context>"},
		{Role: llm.RoleUser, Text: "<system-reminder>ignore this too</system-reminder>"},
	}

	if got := latestMemoryQuery(msgs); got != "Investigate auth regressions" {
		t.Fatalf("latestMemoryQuery() = %q, want original user request", got)
	}
}

func TestManagerSpawnFiltersMemoryBlockForTask(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(cwd)
	}()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	memDir := filepath.Join(dir, ".ageni", "memories")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "build.md"), []byte("---\ndescription: Build help\n---\nRun go build ./...\n"), 0o644); err != nil {
		t.Fatalf("WriteFile build.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "auth.md"), []byte("---\ndescription: Auth help\n---\nJWT middleware lives in internal/auth\n"), 0o644); err != nil {
		t.Fatalf("WriteFile auth.md: %v", err)
	}
	for i := 0; i < 5; i++ {
		name := filepath.Join(memDir, fmt.Sprintf("misc-%d.md", i))
		body := fmt.Sprintf("---\ndescription: Misc %d\n---\nThis memory is unrelated to builds.\n", i)
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}

	memReg, err := memory.Load()
	if err != nil {
		t.Fatalf("memory.Load: %v", err)
	}

	bus := NewBus()
	tracker := llm.NewTracker()
	reg := tools.NewRegistry()
	factory := func(string, []string) (llm.Adapter, string) {
		return &fakeAdapter{scripts: [][]llm.StreamEvent{{{Type: llm.StreamEventDone}}}}, "m"
	}
	mgr := NewManager(context.Background(), bus, reg, tracker, factory, 1)
	mgr.SetMemoryRegistry(memReg)

	id, err := mgr.Spawn(context.Background(), SubagentTask{
		Objective:    "fix the build pipeline",
		OutputFormat: "<result/>",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	sub, ok := mgr.Get(id)
	if !ok {
		t.Fatalf("Get(%q) returned no subagent", id)
	}
	if !strings.Contains(sub.memBlock, "**build**") {
		t.Fatalf("expected build memory in subagent block, got %q", sub.memBlock)
	}
	if strings.Contains(sub.memBlock, "**auth**") {
		t.Fatalf("expected irrelevant auth memory to be filtered out, got %q", sub.memBlock)
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
