package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/ageni/internal/agent"
	"github.com/bouwerp/ageni/internal/llm"
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
		{Sequence: 1, Kind: "subagent_spawn", SubagentID: "s1", SubagentTask: "inspect auth", SubagentModel: "claude-sonnet"},
		{Sequence: 2, Kind: "subagent_text", SubagentID: "s1", Text: "working"},
		{Sequence: 3, Kind: "subagent_paused", SubagentID: "s1"},
		{Sequence: 4, Kind: "subagent_resumed", SubagentID: "s1"},
		{Sequence: 5, Kind: "subagent_done", SubagentID: "s1", Text: "<result>done</result>"},
		{Sequence: 6, Kind: "subagent_spawn", SubagentID: "s2", SubagentTask: "long task", SubagentModel: "claude-haiku"},
		{Sequence: 7, Kind: "subagent_retry", SubagentID: "s2", Text: "retrying"},
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
	if snaps[1].Status != agent.StatusCancelled || !strings.Contains(snaps[1].Buffer, "retry-count") || !strings.Contains(snaps[1].Buffer, "no longer attached") {
		t.Fatalf("worker 2 = %+v", snaps[1])
	}
}

func TestLoadWorkerSnapshotsIncludesRecoveryHintsOnResume(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	writeLogEntries(t, dir, []logEntry{
		{Sequence: 1, Kind: "subagent_spawn", At: now, SubagentID: "s1", SubagentTask: "run tests", SubagentModel: "gpt-5"},
		{Sequence: 2, Kind: "subagent_error", At: now, SubagentID: "s1", Err: "429 rate limit exceeded"},
	})

	snaps, _, err := LoadWorkerSnapshots(&Session{Dir: dir})
	if err != nil {
		t.Fatalf("LoadWorkerSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("len(snaps) = %d, want 1", len(snaps))
	}
	if !strings.Contains(snaps[0].Buffer, "[error_class] rate-limit") {
		t.Fatalf("missing error class in resumed worker snapshot: %+v", snaps[0])
	}
	if !strings.Contains(snaps[0].Buffer, "[recovery_action] respawn_worker") {
		t.Fatalf("missing recovery action in resumed worker snapshot: %+v", snaps[0])
	}
}

func TestLoadSupervisorStateReplaysSequenceAndStatus(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	writeLogEntries(t, dir, []logEntry{
		{Sequence: 4, Kind: "subagent_spawn", At: now, SubagentID: "s2", SubagentTask: "run tests", SubagentModel: "m"},
		{Sequence: 5, Kind: "subagent_retry", At: now, SubagentID: "s2", Text: "retrying"},
		{Sequence: 6, Kind: "subagent_done", At: now, SubagentID: "s2", Text: "<result>done</result>"},
	})

	replay, err := LoadSupervisorState(&Session{Dir: dir})
	if err != nil {
		t.Fatalf("LoadSupervisorState: %v", err)
	}
	if replay.LastSeq != 6 {
		t.Fatalf("LastSeq = %d, want 6", replay.LastSeq)
	}
	snap, ok := replay.State.Worker("s2")
	if !ok {
		t.Fatal("expected worker snapshot for s2")
	}
	if snap.State != agent.SupervisorWorkerDoneUnintegrated {
		t.Fatalf("worker state = %q, want %q", snap.State, agent.SupervisorWorkerDoneUnintegrated)
	}
	if snap.RetryCount != 1 {
		t.Fatalf("retry count = %d, want 1", snap.RetryCount)
	}
}

func TestLoadSupervisorStateReplaysRecoveryMetadata(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	writeLogEntries(t, dir, []logEntry{
		{Sequence: 9, Kind: "subagent_spawn", At: now, SubagentID: "s3", SubagentTask: "run tests", SubagentModel: "gpt-5"},
		{Sequence: 10, Kind: "subagent_error", At: now, SubagentID: "s3", Err: "model not found"},
	})

	replay, err := LoadSupervisorState(&Session{Dir: dir})
	if err != nil {
		t.Fatalf("LoadSupervisorState: %v", err)
	}
	snap, ok := replay.State.Worker("s3")
	if !ok {
		t.Fatal("expected worker snapshot for s3")
	}
	if snap.ErrorClass != llm.ErrorClassModelUnsupported {
		t.Fatalf("error class = %q, want model-unsupported", snap.ErrorClass)
	}
	if snap.RecoveryAction != agent.SupervisorRecoveryUpgradeModel {
		t.Fatalf("recovery action = %q, want %q", snap.RecoveryAction, agent.SupervisorRecoveryUpgradeModel)
	}
}

func TestLoggerSequencePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	sess := &Session{Dir: dir}
	logger, err := NewLogger(sess, LoggerOptions{Mode: LogModePrivate})
	if err != nil {
		t.Fatalf("NewLogger first: %v", err)
	}

	logger.WriteEvent(agent.Event{Kind: agent.EvFlash, Text: "one"})
	logger.WriteEvent(agent.Event{Kind: agent.EvFlash, Text: "two"})
	if err := logger.Close(); err != nil {
		t.Fatalf("Close first logger: %v", err)
	}

	logger, err = NewLogger(sess, LoggerOptions{Mode: LogModePrivate})
	if err != nil {
		t.Fatalf("NewLogger second: %v", err)
	}
	defer logger.Close()
	logger.WriteEvent(agent.Event{Kind: agent.EvFlash, Text: "three"})

	f, err := os.Open(filepath.Join(dir, "log.jsonl")) //nolint:gosec
	if err != nil {
		t.Fatalf("Open log: %v", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var seqs []int64
	for dec.More() {
		var e logEntry
		if err := dec.Decode(&e); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		seqs = append(seqs, e.Sequence)
	}
	if strings.TrimSpace(fmt.Sprint(seqs)) != "[1 2 3]" {
		t.Fatalf("sequence numbers = %v, want [1 2 3]", seqs)
	}
}

func TestLoggerPrivateModeScrubsSensitivePayloads(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewLogger(&Session{Dir: dir}, LoggerOptions{Mode: LogModePrivate})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer logger.Close()

	logger.WriteEvent(agent.Event{
		Kind: agent.EvMasterToolDone,
		ToolCall: &llm.ToolCall{
			Name:      "read_file",
			Arguments: json.RawMessage(`{"path":"secrets.txt","token":"sk-super-secret-token"}`),
		},
		ToolResult: &llm.ToolResult{Content: "Authorization=Bearer abc123secret"},
	})
	logger.WriteEvent(agent.Event{
		Kind:       agent.EvShellOutput,
		SubagentID: "sh1",
		Text:       "Bearer abc123secret\n",
	})

	f, err := os.Open(filepath.Join(dir, "log.jsonl")) //nolint:gosec
	if err != nil {
		t.Fatalf("Open log: %v", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var entries []logEntry
	for dec.More() {
		var e logEntry
		if err := dec.Decode(&e); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		entries = append(entries, e)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if strings.Contains(entries[0].ToolArgs, "sk-super-secret-token") || !strings.Contains(entries[0].ToolArgs, "[redacted tool_args") {
		t.Fatalf("tool args not scrubbed: %q", entries[0].ToolArgs)
	}
	if strings.Contains(entries[0].ToolResult, "abc123secret") || !strings.Contains(entries[0].ToolResult, "[redacted tool_result") {
		t.Fatalf("tool result not scrubbed: %q", entries[0].ToolResult)
	}
	if strings.Contains(entries[1].Text, "abc123secret") || !strings.Contains(entries[1].Text, "[redacted shell output") {
		t.Fatalf("shell output not scrubbed: %q", entries[1].Text)
	}
}

func TestSortIDsNaturalOrder(t *testing.T) {
	ids := []string{"s10", "s1", "s100", "s2", "s20", "sh10", "sh1", "sh100", "sh2"}
	SortIDs(ids)
	expected := []string{"s1", "s2", "s10", "s20", "s100", "sh1", "sh2", "sh10", "sh100"}
	if len(ids) != len(expected) {
		t.Fatalf("length mismatch: got %v, want %v", ids, expected)
	}
	for i := range expected {
		if ids[i] != expected[i] {
			t.Errorf("at index %d: got %q, want %q", i, ids[i], expected[i])
		}
	}
}

