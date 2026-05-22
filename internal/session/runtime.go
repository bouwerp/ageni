package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/ageni/internal/agent"
	"github.com/bouwerp/ageni/internal/llm"
)

type ShellSnapshot struct {
	ID        string
	Label     string
	Kind      agent.ShellKind
	Status    agent.ShellStatus
	Output    string
	LostBytes int64
}

type WorkerSnapshot struct {
	ID        string
	Model     string
	Objective string
	Status    agent.SubagentStatus
	Buffer    string
}

type SupervisorReplay struct {
	State       *agent.SupervisorState
	LastSeq     int64
	LastEventAt time.Time
}

// LoadShellSnapshots reconstructs persisted shell sessions from the append-only session log.
// On resume, shells that were still open are surfaced as closed snapshots because the old
// process is gone; their output remains inspectable in the TUI.
func LoadShellSnapshots(s *Session) ([]ShellSnapshot, int, error) {
	f, err := os.Open(s.Path("log.jsonl")) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	byID := map[string]*ShellSnapshot{}
	order := make([]string, 0, 8)
	maxN := 0
	ensure := func(id string) *ShellSnapshot {
		if sh, ok := byID[id]; ok {
			return sh
		}
		sh := &ShellSnapshot{ID: id, Kind: agent.ShellKindTask, Status: agent.ShellStatusOpen}
		byID[id] = sh
		order = append(order, id)
		if n, err := strconv.Atoi(strings.TrimPrefix(id, "sh")); err == nil && n > maxN {
			maxN = n
		}
		return sh
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e logEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if !strings.HasPrefix(e.SubagentID, "sh") {
			continue
		}
		sh := ensure(e.SubagentID)
		switch e.Kind {
		case "shell_opened":
			sh.Label = e.Text
			if e.ShellKind != "" {
				sh.Kind = agent.ShellKind(e.ShellKind)
			}
			sh.Status = agent.ShellStatusOpen
		case "shell_output":
			sh.Output += e.Text
		case "shell_output_loss":
			if e.Bytes > sh.LostBytes {
				sh.LostBytes = e.Bytes
			}
		case "shell_exited":
			sh.Status = agent.ShellStatusExited
		case "shell_closed":
			sh.Status = agent.ShellStatusClosed
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("read log: %w", err)
	}

	out := make([]ShellSnapshot, 0, len(order))
	for _, id := range order {
		sh := *byID[id]
		if sh.Status == agent.ShellStatusOpen {
			sh.Status = agent.ShellStatusClosed
		}
		out = append(out, sh)
	}
	return out, maxN, nil
}

func PriorShellIDs(snaps []ShellSnapshot) (ids []string, maxN int) {
	for _, sh := range snaps {
		ids = append(ids, sh.ID)
		if n, err := strconv.Atoi(strings.TrimPrefix(sh.ID, "sh")); err == nil && n > maxN {
			maxN = n
		}
	}
	sort.Strings(ids)
	return ids, maxN
}

func LoadWorkerSnapshots(s *Session) ([]WorkerSnapshot, int, error) {
	replay, err := LoadSupervisorState(s)
	if err != nil {
		return nil, 0, err
	}
	if replay.State == nil {
		return nil, 0, nil
	}
	snaps := replay.State.Snapshots()
	out := make([]WorkerSnapshot, 0, len(snaps))
	maxN := 0
	for _, snap := range snaps {
		outSnap := WorkerSnapshot{
			ID:        snap.ID,
			Model:     snap.Model,
			Objective: snap.Objective,
			Status:    mapSupervisorStatus(snap.State),
		}
		if snap.ResultSnippet != "" {
			outSnap.Buffer += "\n[final]\n" + snap.ResultSnippet + "\n"
		}
		if snap.LastError != "" {
			outSnap.Buffer += "\n[error] " + snap.LastError + "\n"
		}
		if snap.ErrorClass != "" && snap.ErrorClass != llm.ErrorClassUnknown {
			outSnap.Buffer += fmt.Sprintf("\n[error_class] %s\n", snap.ErrorClass)
		}
		if snap.RecoveryAction != "" {
			outSnap.Buffer += fmt.Sprintf("\n[recovery_action] %s\n", snap.RecoveryAction)
		}
		if snap.RetryCount > 0 {
			outSnap.Buffer += fmt.Sprintf("\n[retry-count] %d\n", snap.RetryCount)
		}
		if outSnap.Status == agent.StatusRunning || outSnap.Status == agent.StatusPaused {
			outSnap.Status = agent.StatusCancelled
			outSnap.Buffer += "\n[resumed session: worker no longer attached]\n"
		}
		out = append(out, outSnap)
		if n, convErr := strconv.Atoi(strings.TrimPrefix(snap.ID, "s")); convErr == nil && n > maxN {
			maxN = n
		}
	}
	return out, maxN, nil
}

func LoadSupervisorState(s *Session) (SupervisorReplay, error) {
	f, err := os.Open(s.Path("log.jsonl")) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return SupervisorReplay{}, nil
		}
		return SupervisorReplay{}, err
	}
	defer f.Close()
	supervisor := agent.NewSupervisorState(nil)
	replay := SupervisorReplay{State: supervisor}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e logEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		replay.LastSeq = max(replay.LastSeq, e.Sequence)
		if at, err := time.Parse(time.RFC3339Nano, e.At); err == nil && at.After(replay.LastEventAt) {
			replay.LastEventAt = at
		}
		if !strings.HasPrefix(e.SubagentID, "s") {
			continue
		}
		ev := replayEventFromLogEntry(e)
		if !ev.At.IsZero() || ev.Kind != "" {
			supervisor.Replay(ev)
		}
	}
	if err := scanner.Err(); err != nil {
		return SupervisorReplay{}, fmt.Errorf("read log: %w", err)
	}
	if !replay.LastEventAt.IsZero() {
		supervisor.TickAt(replay.LastEventAt)
	}
	return replay, nil
}

func ResumeShellReminder(priorIDs []string, nextN int) string {
	if len(priorIDs) == 0 {
		return ""
	}
	return fmt.Sprintf(`<system-reminder>
Session resumed from disk; prior shell sessions (%s) are NOT live anymore. Do not call shell_exec, shell_read, shell_wait, shell_send_input, interrupt_shell, or close_shell on those IDs as if they were attached to running processes.

Their persisted output is still available in the TUI for inspection. If you need a live shell, open a fresh one. The next shell ID will be sh%d.
</system-reminder>`, strings.Join(priorIDs, ", "), nextN)
}

func replayEventFromLogEntry(e logEntry) agent.Event {
	ev := agent.Event{
		Kind:          agent.EventKind(e.Kind),
		CorrelationID: e.CorrelationID,
		SubagentID:    e.SubagentID,
		Text:          e.Text,
		SubagentTask:  e.SubagentTask,
		SubagentModel: e.SubagentModel,
		ShellKind:     agent.ShellKind(e.ShellKind),
		Bytes:         e.Bytes,
	}
	if e.At != "" {
		if at, err := time.Parse(time.RFC3339Nano, e.At); err == nil {
			ev.At = at
		}
	}
	if e.Err != "" {
		ev.Err = fmt.Errorf("%s", e.Err)
	}
	return ev
}

func mapSupervisorStatus(state agent.SupervisorWorkerState) agent.SubagentStatus {
	switch state {
	case agent.SupervisorWorkerPaused:
		return agent.StatusPaused
	case agent.SupervisorWorkerDoneUnintegrated:
		return agent.StatusDone
	case agent.SupervisorWorkerErrorTerminal, agent.SupervisorWorkerStalled:
		return agent.StatusError
	case agent.SupervisorWorkerCancelled:
		return agent.StatusCancelled
	default:
		return agent.StatusRunning
	}
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
