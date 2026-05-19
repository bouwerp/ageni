package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/bouwerp/ageni/internal/agent"
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
	f, err := os.Open(s.Path("log.jsonl")) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	type workerState struct {
		WorkerSnapshot
	}
	byID := map[string]*workerState{}
	order := make([]string, 0, 8)
	maxN := 0
	ensure := func(id string) *workerState {
		if w, ok := byID[id]; ok {
			return w
		}
		w := &workerState{WorkerSnapshot: WorkerSnapshot{ID: id, Status: agent.StatusRunning}}
		byID[id] = w
		order = append(order, id)
		if n, err := strconv.Atoi(strings.TrimPrefix(id, "s")); err == nil && n > maxN {
			maxN = n
		}
		return w
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
		if !strings.HasPrefix(e.SubagentID, "s") {
			continue
		}
		w := ensure(e.SubagentID)
		switch e.Kind {
		case "subagent_spawn":
			w.Model = e.SubagentModel
			w.Objective = e.SubagentTask
			w.Status = agent.StatusRunning
		case "subagent_text":
			w.Buffer += e.Text
		case "subagent_tool_call":
			w.Buffer += fmt.Sprintf("\n[tool] %s %s\n", e.ToolName, strings.TrimSpace(e.ToolArgs))
		case "subagent_tool_done":
			suffix := ""
			if e.ToolError {
				suffix = " error"
			}
			w.Buffer += fmt.Sprintf("\n[tool-result%s]\n%s\n", suffix, e.ToolResult)
		case "subagent_retry":
			w.Buffer += "\n[retry] " + e.Text + "\n"
		case "subagent_inbox":
			w.Buffer += "\n[inbox] " + e.Text + "\n"
		case "subagent_paused":
			w.Status = agent.StatusPaused
			w.Buffer += "\n[paused]\n"
		case "subagent_resumed":
			w.Status = agent.StatusRunning
			w.Buffer += "\n[resumed]\n"
		case "subagent_done":
			w.Status = agent.StatusDone
			if e.Text != "" {
				w.Buffer += "\n[final]\n" + e.Text + "\n"
			}
		case "subagent_error":
			w.Status = agent.StatusError
			w.Buffer += "\n[error] " + e.Err + "\n"
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("read log: %w", err)
	}

	out := make([]WorkerSnapshot, 0, len(order))
	for _, id := range order {
		w := byID[id].WorkerSnapshot
		if w.Status == agent.StatusRunning || w.Status == agent.StatusPaused {
			w.Status = agent.StatusCancelled
			w.Buffer += "\n[resumed session: worker no longer attached]\n"
		}
		out = append(out, w)
	}
	return out, maxN, nil
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
