package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/ageni/internal/agent"
)

func writeLogEntries(t *testing.T, dir string, entries []logEntry) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "log.jsonl"))
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode log entry: %v", err)
		}
	}
}

func TestLoadShellSnapshots(t *testing.T) {
	dir := t.TempDir()
	writeLogEntries(t, dir, []logEntry{
		{Kind: "shell_opened", SubagentID: "sh1", Text: "server", ShellKind: string(agent.ShellKindService)},
		{Kind: "shell_output", SubagentID: "sh1", Text: "ready\n"},
		{Kind: "shell_output_loss", SubagentID: "sh1", Text: "wrapped", Bytes: 1234},
		{Kind: "shell_exited", SubagentID: "sh1"},
		{Kind: "shell_opened", SubagentID: "sh2", Text: "task"},
		{Kind: "shell_output", SubagentID: "sh2", Text: "still here\n"},
	})

	snaps, maxN, err := LoadShellSnapshots(&Session{Dir: dir})
	if err != nil {
		t.Fatalf("LoadShellSnapshots: %v", err)
	}
	if maxN != 2 {
		t.Fatalf("maxN = %d, want 2", maxN)
	}
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	if snaps[0].Kind != agent.ShellKindService || snaps[0].Status != agent.ShellStatusExited {
		t.Fatalf("shell 1 = %+v", snaps[0])
	}
	if snaps[0].LostBytes != 1234 || !strings.Contains(snaps[0].Output, "ready") {
		t.Fatalf("shell 1 persistence wrong: %+v", snaps[0])
	}
	if snaps[1].Status != agent.ShellStatusClosed {
		t.Fatalf("shell 2 should resume as closed, got %+v", snaps[1])
	}
	ids, maxShell := PriorShellIDs(snaps)
	if maxShell != 2 || strings.Join(ids, ",") != "sh1,sh2" {
		t.Fatalf("PriorShellIDs = %v / %d", ids, maxShell)
	}
	if reminder := ResumeShellReminder(ids, 3); !strings.Contains(reminder, "interrupt_shell") || !strings.Contains(reminder, "sh3") {
		t.Fatalf("unexpected reminder: %s", reminder)
	}
}

func TestLoadWorkerSnapshots(t *testing.T) {
	dir := t.TempDir()
	writeLogEntries(t, dir, []logEntry{
		{Kind: "subagent_spawn", SubagentID: "s1", SubagentTask: "inspect auth", SubagentModel: "claude-sonnet"},
		{Kind: "subagent_text", SubagentID: "s1", Text: "working"},
		{Kind: "subagent_paused", SubagentID: "s1"},
		{Kind: "subagent_resumed", SubagentID: "s1"},
		{Kind: "subagent_done", SubagentID: "s1", Text: "<result>done</result>"},
		{Kind: "subagent_spawn", SubagentID: "s2", SubagentTask: "long task", SubagentModel: "claude-haiku"},
		{Kind: "subagent_text", SubagentID: "s2", Text: "partial"},
	})

	snaps, maxN, err := LoadWorkerSnapshots(&Session{Dir: dir})
	if err != nil {
		t.Fatalf("LoadWorkerSnapshots: %v", err)
	}
	if maxN != 2 {
		t.Fatalf("maxN = %d, want 2", maxN)
	}
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	if snaps[0].Status != agent.StatusDone || !strings.Contains(snaps[0].Buffer, "<result>done</result>") {
		t.Fatalf("worker 1 = %+v", snaps[0])
	}
	if snaps[1].Status != agent.StatusCancelled || !strings.Contains(snaps[1].Buffer, "no longer attached") {
		t.Fatalf("worker 2 = %+v", snaps[1])
	}
}
