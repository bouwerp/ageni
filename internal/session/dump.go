package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// FormatLog reads a session log.jsonl file and writes a human-readable
// chronological transcript to w. Output groups entries by speaker (master
// vs each sub-agent) and renders tool calls + results as indented blocks.
//
// This is the format used by `ageni sessions dump <id>` and the F3 keybind.
// It's lossy — token usage, cache stats, and exact timings are summarised
// rather than enumerated — but it's what the user wants when pasting a
// session into a chat for debugging.
func FormatLog(s *Session, w io.Writer) error {
	logPath := s.Path("log.jsonl")
	f, err := os.Open(logPath) //nolint:gosec
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()

	fmt.Fprintf(bw, "ageni session %s\n", s.ID)
	if !s.Started.IsZero() {
		fmt.Fprintf(bw, "started:   %s\n", s.Started.Format(time.RFC3339))
	}
	if !s.LastUsed.IsZero() {
		fmt.Fprintf(bw, "last used: %s\n", s.LastUsed.Format(time.RFC3339))
	}
	if s.RepoPath != "" {
		fmt.Fprintf(bw, "repo:      %s\n", s.RepoPath)
	}
	if s.MasterModel != "" {
		fmt.Fprintf(bw, "master:    %s/%s\n", s.MasterProvider, s.MasterModel)
	}
	if s.SubagentModel != "" {
		fmt.Fprintf(bw, "sub-agent: %s/%s\n", s.SubagentProvider, s.SubagentModel)
	}
	fmt.Fprintf(bw, "log:       %s\n", logPath)
	fmt.Fprintln(bw, strings.Repeat("─", 72))

	// Coalesce streaming text deltas. The log records every chunk emitted
	// by the LLM stream as a separate entry, which would dump line-by-line
	// fragments. We accumulate runs of text from the same speaker and flush
	// when the speaker changes or a non-text event arrives.
	var accumSpeaker string
	var accum strings.Builder

	flush := func() {
		if accum.Len() == 0 {
			return
		}
		text := strings.TrimRight(accum.String(), "\n")
		fmt.Fprintf(bw, "\n%s ❯\n%s\n", accumSpeaker, indentBy2(text))
		accum.Reset()
		accumSpeaker = ""
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
			fmt.Fprintf(bw, "\n[malformed log entry: %v]\n", err)
			continue
		}
		ts := shortTime(e.At)
		switch e.Kind {
		case "user_message":
			flush()
			fmt.Fprintf(bw, "\n[%s] you ❯\n%s\n", ts, indentBy2(e.Text))
		case "master_text":
			if accumSpeaker != "master" {
				flush()
				accumSpeaker = "master"
			}
			accum.WriteString(e.Text)
		case "subagent_text":
			speaker := "sub:" + e.SubagentID
			if accumSpeaker != speaker {
				flush()
				accumSpeaker = speaker
			}
			accum.WriteString(e.Text)
		case "master_tool_call":
			flush()
			fmt.Fprintf(bw, "\n[%s] master → %s%s\n", ts, e.ToolName, formatArgs(e.ToolArgs))
		case "master_tool_done":
			fmt.Fprintf(bw, "[%s] master ← %s%s\n%s\n", ts, e.ToolName, errMark(e.ToolError), indentBy2(truncate(e.ToolResult, 4000)))
		case "subagent_spawn":
			flush()
			fmt.Fprintf(bw, "\n[%s] spawn %s (model=%s) — %s\n", ts, e.SubagentID, e.SubagentModel, e.SubagentTask)
		case "subagent_tool_call":
			flush()
			fmt.Fprintf(bw, "\n[%s] %s → %s%s\n", ts, e.SubagentID, e.ToolName, formatArgs(e.ToolArgs))
		case "subagent_tool_done":
			fmt.Fprintf(bw, "[%s] %s ← %s%s\n%s\n", ts, e.SubagentID, e.ToolName, errMark(e.ToolError), indentBy2(truncate(e.ToolResult, 4000)))
		case "subagent_done":
			flush()
			if e.Text != "" {
				fmt.Fprintf(bw, "\n[%s] %s done ❯\n%s\n", ts, e.SubagentID, indentBy2(e.Text))
			} else {
				fmt.Fprintf(bw, "\n[%s] %s done (no final text)\n", ts, e.SubagentID)
			}
		case "subagent_paused":
			flush()
			fmt.Fprintf(bw, "[%s] %s paused\n", ts, e.SubagentID)
		case "subagent_resumed":
			flush()
			fmt.Fprintf(bw, "[%s] %s resumed\n", ts, e.SubagentID)
		case "subagent_error":
			flush()
			fmt.Fprintf(bw, "\n[%s] %s ERROR: %s\n", ts, e.SubagentID, e.Err)
		case "subagent_retry":
			flush()
			fmt.Fprintf(bw, "[%s] %s retry: %s\n", ts, e.SubagentID, e.Text)
		case "subagent_inbox":
			flush()
			fmt.Fprintf(bw, "[%s] master → %s: %s\n", ts, e.SubagentID, oneLine(e.Text))
		case "master_turn_done":
			flush()
		case "master_paused":
			flush()
			fmt.Fprintf(bw, "\n[%s] master paused\n", ts)
		case "master_resumed":
			flush()
			fmt.Fprintf(bw, "\n[%s] master resumed\n", ts)
		case "shell_opened":
			flush()
			label := e.SubagentID
			if e.Text != "" {
				label += " " + e.Text
			}
			kind := e.ShellKind
			if kind == "" {
				kind = "task"
			}
			fmt.Fprintf(bw, "\n[%s] shell %s opened (%s)\n", ts, label, kind)
		case "shell_output":
			speaker := "shell:" + e.SubagentID
			if accumSpeaker != speaker {
				flush()
				accumSpeaker = speaker
			}
			accum.WriteString(e.Text)
		case "shell_output_loss":
			flush()
			fmt.Fprintf(bw, "[%s] shell %s warning: %s\n", ts, e.SubagentID, oneLine(e.Text))
		case "shell_exited":
			flush()
			fmt.Fprintf(bw, "\n[%s] shell %s exited\n", ts, e.SubagentID)
		case "shell_closed":
			flush()
			fmt.Fprintf(bw, "\n[%s] shell %s closed\n", ts, e.SubagentID)
		case "error":
			flush()
			fmt.Fprintf(bw, "\n[%s] ERROR: %s\n", ts, e.Err)
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read log: %w", err)
	}
	fmt.Fprintln(bw, strings.Repeat("─", 72))
	fmt.Fprintln(bw, "end of session log")
	return nil
}

// logEntry mirrors the on-disk JSONL shape from log.go.entry. Kept private
// here so the session package can read its own logs without exposing the
// shape to callers.
type logEntry struct {
	Kind          string `json:"kind"`
	At            string `json:"at"`
	SubagentID    string `json:"subagent_id,omitempty"`
	Text          string `json:"text,omitempty"`
	ToolName      string `json:"tool_name,omitempty"`
	ToolArgs      string `json:"tool_args,omitempty"`
	ToolResult    string `json:"tool_result,omitempty"`
	ToolError     bool   `json:"tool_error,omitempty"`
	ShellKind     string `json:"shell_kind,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	SubagentTask  string `json:"subagent_task,omitempty"`
	SubagentModel string `json:"subagent_model,omitempty"`
	Err           string `json:"err,omitempty"`
}

func shortTime(rfc string) string {
	t, err := time.Parse(time.RFC3339Nano, rfc)
	if err != nil {
		return rfc
	}
	return t.Format("15:04:05")
}

// indentBy2 prefixes every line of s with two spaces. Used to nest tool
// output, message bodies, and result blocks under their speaker headers.
func indentBy2(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = "  " + ln
	}
	return strings.Join(lines, "\n")
}

func formatArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return "()"
	}
	// Try to pretty-print compact JSON; fall back to single-line raw.
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		if b, err := json.Marshal(v); err == nil {
			return "(" + string(b) + ")"
		}
	}
	return "(" + oneLine(raw) + ")"
}

func errMark(b bool) string {
	if b {
		return " [error]"
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n  …(truncated, %d more bytes)", len(s)-max)
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}
