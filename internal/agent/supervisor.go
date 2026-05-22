package agent

import (
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/ageni/internal/llm"
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
	SupervisorDecisionNone               SupervisorDecision = ""
	SupervisorDecisionIntegrateResult    SupervisorDecision = "integrate_result"
	SupervisorDecisionNudgeStalledWorker SupervisorDecision = "nudge_stalled_worker"
	SupervisorDecisionEscalateError      SupervisorDecision = "escalate_error"
	SupervisorDecisionEscalateStall      SupervisorDecision = "escalate_stall"
)

type SupervisorRecoveryAction string

const (
	SupervisorRecoveryNone          SupervisorRecoveryAction = ""
	SupervisorRecoveryNudgeWorker   SupervisorRecoveryAction = "nudge_worker"
	SupervisorRecoveryRespawnWorker SupervisorRecoveryAction = "respawn_worker"
	SupervisorRecoveryUpgradeModel  SupervisorRecoveryAction = "upgrade_model"
	SupervisorRecoveryFixProvider   SupervisorRecoveryAction = "fix_provider"
	SupervisorRecoveryReplanTask    SupervisorRecoveryAction = "replan_task"
	SupervisorRecoveryInvestigate   SupervisorRecoveryAction = "investigate"
)

type SupervisorWorkerSnapshot struct {
	ID             string
	Objective      string
	Model          string
	State          SupervisorWorkerState
	LastEventKind  EventKind
	LastEventAt    time.Time
	LastProgress   time.Time
	RetryCount     int
	StallCount     int
	LastError      string
	ErrorClass     llm.ErrorClass
	RecoveryAction SupervisorRecoveryAction
	ResultSnippet  string
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
		snap.StallCount = 0
		snap.LastError = ""
		snap.ErrorClass = llm.ErrorClassUnknown
		snap.RecoveryAction = SupervisorRecoveryNone
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
		snap.RecoveryAction = SupervisorRecoveryNone
		progress = true
		decision = SupervisorDecisionIntegrateResult
	case EvSubagentError:
		snap.State = SupervisorWorkerErrorTerminal
		if ev.Err != nil {
			snap.LastError = ev.Err.Error()
		}
		snap.ErrorClass, snap.RecoveryAction = classifySupervisorRecovery(ev.Err)
		decision = SupervisorDecisionEscalateError
	case EvCancelAll:
		snap.State = SupervisorWorkerCancelled
		snap.RecoveryAction = SupervisorRecoveryNone
	}
	if progress {
		snap.LastProgress = now
		snap.StallCount = 0
		snap.ErrorClass = llm.ErrorClassUnknown
		snap.RecoveryAction = SupervisorRecoveryNone
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
				snap.StallCount++
				if snap.StallCount == 1 {
					snap.RecoveryAction = SupervisorRecoveryNudgeWorker
					s.workers[id] = snap
					return SupervisorDecisionNudgeStalledWorker, id
				}
				snap.RecoveryAction = SupervisorRecoveryRespawnWorker
				s.workers[id] = snap
				return SupervisorDecisionEscalateStall, id
			}
		case SupervisorWorkerStalled:
			if !snap.LastProgress.IsZero() && now.Sub(snap.LastProgress) >= s.stalledAfter {
				snap.LastEventAt = now
				snap.StallCount++
				snap.RecoveryAction = SupervisorRecoveryRespawnWorker
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

func classifySupervisorRecovery(err error) (llm.ErrorClass, SupervisorRecoveryAction) {
	class := llm.ClassifyError(err)
	switch class {
	case llm.ErrorClassDeadlineExceeded,
		llm.ErrorClassRateLimit,
		llm.ErrorClassServer,
		llm.ErrorClassNetwork:
		return class, SupervisorRecoveryRespawnWorker
	case llm.ErrorClassContextLimit,
		llm.ErrorClassModelUnsupported:
		return class, SupervisorRecoveryUpgradeModel
	case llm.ErrorClassAuth,
		llm.ErrorClassPermission,
		llm.ErrorClassPayment:
		return class, SupervisorRecoveryFixProvider
	case llm.ErrorClassInvalidRequest,
		llm.ErrorClassNotFound:
		return class, SupervisorRecoveryReplanTask
	case llm.ErrorClassCancelled:
		return class, SupervisorRecoveryNone
	default:
		return class, SupervisorRecoveryInvestigate
	}
}
