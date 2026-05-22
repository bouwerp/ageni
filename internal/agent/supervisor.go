package agent

import (
	"sort"
	"strings"
	"time"
)

type SupervisorWorkerState string

const (
	SupervisorWorkerUnknown          SupervisorWorkerState = "unknown"
	SupervisorWorkerRunning          SupervisorWorkerState = "running"
	SupervisorWorkerThinking         SupervisorWorkerState = "thinking"
	SupervisorWorkerWaitingOnTool    SupervisorWorkerState = "waiting_on_tool"
	SupervisorWorkerPaused           SupervisorWorkerState = "paused"
	SupervisorWorkerDoneUnintegrated SupervisorWorkerState = "done_unintegrated"
	SupervisorWorkerErrorTerminal    SupervisorWorkerState = "error_terminal"
	SupervisorWorkerCancelled        SupervisorWorkerState = "cancelled"
	SupervisorWorkerStalled          SupervisorWorkerState = "stalled"
)

type SupervisorDecision string

const (
	SupervisorDecisionNone            SupervisorDecision = ""
	SupervisorDecisionIntegrateResult SupervisorDecision = "integrate_result"
	SupervisorDecisionEscalateError   SupervisorDecision = "escalate_error"
	SupervisorDecisionEscalateStall   SupervisorDecision = "escalate_stall"
)

type SupervisorWorkerSnapshot struct {
	ID            string
	Objective     string
	Model         string
	State         SupervisorWorkerState
	LastEventKind EventKind
	LastEventAt   time.Time
	LastProgress  time.Time
	RetryCount    int
	LastError     string
	ResultSnippet string
}

type SupervisorState struct {
	now          func() time.Time
	stalledAfter time.Duration
	workers      map[string]SupervisorWorkerSnapshot
}

func NewSupervisorState(now func() time.Time) *SupervisorState {
	if now == nil {
		now = time.Now
	}
	return &SupervisorState{
		now:          now,
		stalledAfter: 2 * time.Minute,
		workers:      make(map[string]SupervisorWorkerSnapshot),
	}
}

func (s *SupervisorState) Worker(id string) (SupervisorWorkerSnapshot, bool) {
	snap, ok := s.workers[id]
	return snap, ok
}

func (s *SupervisorState) Observe(ev Event) SupervisorDecision {
	return s.observeAt(s.now(), ev)
}

func (s *SupervisorState) Replay(ev Event) SupervisorDecision {
	at := ev.At
	if at.IsZero() {
		at = s.now()
	}
	return s.observeAt(at, ev)
}

func (s *SupervisorState) observeAt(now time.Time, ev Event) SupervisorDecision {
	if s == nil || ev.SubagentID == "" || !strings.HasPrefix(ev.SubagentID, "s") {
		return SupervisorDecisionNone
	}
	snap := s.workers[ev.SubagentID]
	if snap.ID == "" {
		snap.ID = ev.SubagentID
	}
	if snap.LastProgress.IsZero() {
		snap.LastProgress = now
	}
	snap.LastEventAt = now
	snap.LastEventKind = ev.Kind

	progress := false
	decision := SupervisorDecisionNone

	switch ev.Kind {
	case EvSubagentSpawn:
		snap.Objective = ev.SubagentTask
		snap.Model = ev.SubagentModel
		snap.State = SupervisorWorkerRunning
		progress = true
	case EvSubagentTurnStart:
		snap.State = SupervisorWorkerThinking
		progress = true
	case EvSubagentText, EvSubagentUsage, EvSubagentInbox:
		snap.State = SupervisorWorkerRunning
		progress = true
	case EvSubagentToolCall:
		snap.State = SupervisorWorkerWaitingOnTool
		progress = true
	case EvSubagentToolDone:
		snap.State = SupervisorWorkerRunning
		progress = true
	case EvSubagentRetry:
		snap.State = SupervisorWorkerRunning
		snap.RetryCount++
		progress = true
	case EvSubagentPaused:
		snap.State = SupervisorWorkerPaused
	case EvSubagentResumed:
		snap.State = SupervisorWorkerRunning
		progress = true
	case EvSubagentDone:
		snap.State = SupervisorWorkerDoneUnintegrated
		snap.ResultSnippet = clipSupervisorText(ev.Text, 240)
		progress = true
		decision = SupervisorDecisionIntegrateResult
	case EvSubagentError:
		snap.State = SupervisorWorkerErrorTerminal
		if ev.Err != nil {
			snap.LastError = ev.Err.Error()
		}
		decision = SupervisorDecisionEscalateError
	case EvCancelAll:
		snap.State = SupervisorWorkerCancelled
	}
	if progress {
		snap.LastProgress = now
	}
	s.workers[ev.SubagentID] = snap
	return decision
}

func (s *SupervisorState) Tick() (SupervisorDecision, string) {
	return s.TickAt(s.now())
}

func (s *SupervisorState) TickAt(now time.Time) (SupervisorDecision, string) {
	if s == nil {
		return SupervisorDecisionNone, ""
	}
	if s.stalledAfter <= 0 {
		s.stalledAfter = 2 * time.Minute
	}
	ids := make([]string, 0, len(s.workers))
	for id := range s.workers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		snap := s.workers[id]
		switch snap.State {
		case SupervisorWorkerRunning, SupervisorWorkerThinking, SupervisorWorkerWaitingOnTool:
			if !snap.LastProgress.IsZero() && now.Sub(snap.LastProgress) >= s.stalledAfter {
				snap.State = SupervisorWorkerStalled
				snap.LastEventAt = now
				s.workers[id] = snap
				return SupervisorDecisionEscalateStall, id
			}
		}
	}
	return SupervisorDecisionNone, ""
}

func (s *SupervisorState) Snapshots() []SupervisorWorkerSnapshot {
	if s == nil {
		return nil
	}
	ids := make([]string, 0, len(s.workers))
	for id := range s.workers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]SupervisorWorkerSnapshot, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.workers[id])
	}
	return out
}

func clipSupervisorText(text string, max int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if max > 0 && len(text) > max {
		return text[:max] + "…"
	}
	return text
}
