package agent

import (
	"errors"
	"testing"
	"time"
)

func TestSupervisorTracksWorkerLifecycle(t *testing.T) {
	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	supervisor := NewSupervisorState(func() time.Time { return now })

	if got := supervisor.Observe(Event{Kind: EvSubagentSpawn, SubagentID: "s1", SubagentTask: "inspect auth", SubagentModel: "m"}); got != SupervisorDecisionNone {
		t.Fatalf("spawn decision = %q, want none", got)
	}
	if got := supervisor.Observe(Event{Kind: EvSubagentTurnStart, SubagentID: "s1"}); got != SupervisorDecisionNone {
		t.Fatalf("turn start decision = %q, want none", got)
	}
	if got := supervisor.Observe(Event{Kind: EvSubagentToolCall, SubagentID: "s1"}); got != SupervisorDecisionNone {
		t.Fatalf("tool call decision = %q, want none", got)
	}
	if got := supervisor.Observe(Event{Kind: EvSubagentToolDone, SubagentID: "s1"}); got != SupervisorDecisionNone {
		t.Fatalf("tool done decision = %q, want none", got)
	}
	if got := supervisor.Observe(Event{Kind: EvSubagentRetry, SubagentID: "s1", Text: "retrying"}); got != SupervisorDecisionNone {
		t.Fatalf("retry decision = %q, want none", got)
	}

	snap, ok := supervisor.Worker("s1")
	if !ok {
		t.Fatal("expected worker snapshot for s1")
	}
	if snap.State != SupervisorWorkerRunning {
		t.Fatalf("worker state = %q, want %q", snap.State, SupervisorWorkerRunning)
	}
	if snap.RetryCount != 1 {
		t.Fatalf("retry count = %d, want 1", snap.RetryCount)
	}
	if snap.Objective != "inspect auth" || snap.Model != "m" {
		t.Fatalf("worker metadata = %+v", snap)
	}

	if got := supervisor.Observe(Event{Kind: EvSubagentDone, SubagentID: "s1", Text: "<result>done</result>"}); got != SupervisorDecisionIntegrateResult {
		t.Fatalf("done decision = %q, want %q", got, SupervisorDecisionIntegrateResult)
	}
	snap, ok = supervisor.Worker("s1")
	if !ok {
		t.Fatal("expected worker snapshot for s1 after done")
	}
	if snap.State != SupervisorWorkerDoneUnintegrated {
		t.Fatalf("done state = %q, want %q", snap.State, SupervisorWorkerDoneUnintegrated)
	}
	if snap.ResultSnippet == "" {
		t.Fatalf("expected result snippet to be captured, got empty snapshot: %+v", snap)
	}
}

func TestSupervisorEscalatesErrorsAndStalls(t *testing.T) {
	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	supervisor := NewSupervisorState(func() time.Time { return now })
	supervisor.stalledAfter = 30 * time.Second

	supervisor.Observe(Event{Kind: EvSubagentSpawn, SubagentID: "s1", SubagentTask: "run tests"})
	now = now.Add(31 * time.Second)

	decision, stalled := supervisor.Tick()
	if decision != SupervisorDecisionEscalateStall {
		t.Fatalf("stall decision = %q, want %q", decision, SupervisorDecisionEscalateStall)
	}
	if stalled != "s1" {
		t.Fatalf("stalled worker = %q, want s1", stalled)
	}
	snap, ok := supervisor.Worker("s1")
	if !ok {
		t.Fatal("expected worker snapshot for stalled worker")
	}
	if snap.State != SupervisorWorkerStalled {
		t.Fatalf("stalled state = %q, want %q", snap.State, SupervisorWorkerStalled)
	}

	if got := supervisor.Observe(Event{Kind: EvSubagentError, SubagentID: "s1", Err: errors.New("boom")}); got != SupervisorDecisionEscalateError {
		t.Fatalf("error decision = %q, want %q", got, SupervisorDecisionEscalateError)
	}
	snap, ok = supervisor.Worker("s1")
	if !ok {
		t.Fatal("expected worker snapshot for errored worker")
	}
	if snap.State != SupervisorWorkerErrorTerminal {
		t.Fatalf("error state = %q, want %q", snap.State, SupervisorWorkerErrorTerminal)
	}
	if snap.LastError != "boom" {
		t.Fatalf("last error = %q, want boom", snap.LastError)
	}
}
