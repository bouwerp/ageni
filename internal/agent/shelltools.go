package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// OpenShellTool opens a new shell session.
type OpenShellTool struct {
	SM *ShellManager
}

func (t OpenShellTool) Name() string { return "open_shell" }
func (t OpenShellTool) Description() string {
	return "Open a new persistent bash shell session. Returns the session ID for use with shell_exec, shell_read, etc."
}
func (t OpenShellTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"label": {
				"type": "string",
				"description": "Human-readable name for this shell, e.g. 'Metro Server', 'Webpack Dev'. Optional but recommended."
			},
			"kind": {
				"type": "string",
				"enum": ["task", "service"],
				"description": "task (default) = short-lived command; service = long-running server or daemon that should stay visible in the UI."
			}
		},
		"required": []
	}`)
}
func (t OpenShellTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Label string `json:"label"`
		Kind  string `json:"kind"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	kind := ShellKind(a.Kind)
	if kind != ShellKindService {
		kind = ShellKindTask
	}
	s, err := t.SM.Open(a.Label, kind)
	if err != nil {
		return "", fmt.Errorf("open shell: %w", err)
	}
	desc := string(kind)
	if a.Label != "" {
		desc = fmt.Sprintf("%s (%s)", a.Label, kind)
	}
	return fmt.Sprintf("opened shell session %s [%s]", s.ID(), desc), nil
}

// ShellExecTool executes a command in a shell session.
type ShellExecTool struct {
	SM *ShellManager
}

func (t ShellExecTool) Name() string { return "shell_exec" }
func (t ShellExecTool) Description() string {
	return "Execute a command in a persistent shell session. Use mode=sync (default) to wait for completion, mode=async for fire-and-forget."
}
func (t ShellExecTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Shell session ID"},
			"command": {"type": "string", "description": "Command to execute"},
			"mode": {"type": "string", "enum": ["sync", "async"], "default": "sync"},
			"timeout_seconds": {"type": "number", "description": "Timeout in seconds (sync mode only)"}
		},
		"required": ["id", "command"]
	}`)
}
func (t ShellExecTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID             string  `json:"id"`
		Command        string  `json:"command"`
		Mode           string  `json:"mode"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if a.ID == "" {
		a.ID = ResolveShellID(args)
	}
	s, ok := t.SM.Get(a.ID)
	if !ok {
		return "", fmt.Errorf("no such shell: %s", a.ID)
	}
	waitDone := a.Mode != "async"
	var timeout time.Duration
	if a.TimeoutSeconds > 0 {
		timeout = time.Duration(a.TimeoutSeconds * float64(time.Second))
	}
	return s.Exec(ctx, a.Command, timeout, waitDone)
}

// ShellReadTool reads output from a shell session.
type ShellReadTool struct {
	SM *ShellManager
}

func (t ShellReadTool) Name() string { return "shell_read" }
func (t ShellReadTool) Description() string {
	return "Read output from a shell session. Use tail_lines for the last N lines, or offset+max_bytes for incremental reads."
}
func (t ShellReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Shell session ID"},
			"tail_lines": {"type": "integer", "description": "Return last N lines (default 50)"},
			"offset": {"type": "integer", "description": "Global byte offset to read from"},
			"max_bytes": {"type": "integer", "description": "Max bytes to return (default 8192)"}
		},
		"required": ["id"]
	}`)
}
func (t ShellReadTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID        string `json:"id"`
		TailLines int    `json:"tail_lines"`
		Offset    *int64 `json:"offset"`
		MaxBytes  int    `json:"max_bytes"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if a.ID == "" {
		a.ID = ResolveShellID(args)
	}
	s, ok := t.SM.Get(a.ID)
	if !ok {
		return "", fmt.Errorf("no such shell: %s", a.ID)
	}

	if a.Offset != nil {
		maxBytes := a.MaxBytes
		if maxBytes <= 0 {
			maxBytes = 8192
		}
		base := s.BaseOffset()
		data, nextOffset := s.ReadFrom(*a.Offset, maxBytes)
		if *a.Offset < base {
			return fmt.Sprintf("warning: shell buffer dropped oldest %d bytes; read resumed at offset %d\noffset=%d\n%s", s.LostBytes(), base, nextOffset, string(data)), nil
		}
		return fmt.Sprintf("offset=%d\n%s", nextOffset, string(data)), nil
	}

	tailLines := a.TailLines
	if tailLines <= 0 {
		tailLines = 50
	}
	return s.TailLines(tailLines), nil
}

// ShellWaitTool waits for a pattern in shell output.
type ShellWaitTool struct {
	SM *ShellManager
}

func (t ShellWaitTool) Name() string { return "shell_wait" }
func (t ShellWaitTool) Description() string {
	return "Wait until a pattern appears in the shell output. Useful for waiting for servers to start, build completion, etc."
}
func (t ShellWaitTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Shell session ID"},
			"pattern": {"type": "string", "description": "Pattern to wait for"},
			"offset": {"type": "integer", "description": "Start scanning from this global offset"},
			"timeout_seconds": {"type": "number", "description": "Timeout in seconds"}
		},
		"required": ["id", "pattern"]
	}`)
}
func (t ShellWaitTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID             string  `json:"id"`
		Pattern        string  `json:"pattern"`
		Offset         *int64  `json:"offset"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if a.ID == "" {
		a.ID = ResolveShellID(args)
	}
	s, ok := t.SM.Get(a.ID)
	if !ok {
		return "", fmt.Errorf("no such shell: %s", a.ID)
	}
	var startOffset int64
	if a.Offset != nil {
		startOffset = *a.Offset
	}
	var timeout time.Duration
	if a.TimeoutSeconds > 0 {
		timeout = time.Duration(a.TimeoutSeconds * float64(time.Second))
	}
	endOffset, err := s.WaitForPattern(ctx, a.Pattern, startOffset, timeout)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pattern found at offset %d", endOffset), nil
}

// ShellSendInputTool sends raw input or named key sequences to a shell session's stdin.
type ShellSendInputTool struct {
	SM *ShellManager
}

func (t ShellSendInputTool) Name() string { return "shell_send_input" }
func (t ShellSendInputTool) Description() string {
	return "Send raw text or named key sequences to a shell session's stdin. Use 'input' for raw text, or 'keys' for named keys like ctrl+c, enter, arrow keys, etc. Useful for interacting with running processes (dev servers, prompts, TUI apps)."
}
func (t ShellSendInputTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Shell session ID"},
			"input": {"type": "string", "description": "Raw text to write to stdin. JSON escape sequences (\\r, \\t, \\x03, etc.) are honoured."},
			"keys": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Sequence of named keys to send. Each element is either a named key or a literal character. Named keys: 'enter', 'escape', 'tab', 'backspace', 'space', 'up', 'down', 'left', 'right', 'home', 'end', 'page_up', 'page_down', 'ctrl+a'…'ctrl+z', 'delete', 'f1'…'f12'. Literal example: ['r'] sends the character 'r'; ['r','enter'] sends 'r' then Enter. Keys are sent in order after 'input' (if any)."
			}
		},
		"required": ["id"]
	}`)
}
func (t ShellSendInputTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID    string   `json:"id"`
		Input string   `json:"input"`
		Keys  []string `json:"keys"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if a.ID == "" {
		a.ID = ResolveShellID(args)
	}
	s, ok := t.SM.Get(a.ID)
	if !ok {
		return "", fmt.Errorf("no such shell: %s", a.ID)
	}
	if a.Input == "" && len(a.Keys) == 0 {
		return "", fmt.Errorf("provide 'input' text or 'keys' array")
	}
	// Send raw input first, then named keys.
	if a.Input != "" {
		if err := s.SendInput(a.Input); err != nil {
			return "", err
		}
	}
	if len(a.Keys) > 0 {
		if err := s.SendKeys(a.Keys); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("sent to %s", a.ID), nil
}

// CloseShellTool closes a shell session.
type CloseShellTool struct {
	SM *ShellManager
}

func (t CloseShellTool) Name() string        { return "close_shell" }
func (t CloseShellTool) Description() string { return "Close a persistent shell session." }
func (t CloseShellTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Shell session ID"}
		},
		"required": ["id"]
	}`)
}
func (t CloseShellTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if a.ID == "" {
		a.ID = ResolveShellID(args)
	}
	if err := t.SM.Close(a.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("shell %s closed", a.ID), nil
}

// InterruptShellTool sends an interrupt signal to a running shell session.
type InterruptShellTool struct {
	SM *ShellManager
}

func (t InterruptShellTool) Name() string { return "interrupt_shell" }
func (t InterruptShellTool) Description() string {
	return "Send an interrupt signal (SIGINT / Ctrl+C equivalent) to a running shell session."
}
func (t InterruptShellTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Shell session ID"}
		},
		"required": ["id"]
	}`)
}
func (t InterruptShellTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if a.ID == "" {
		a.ID = ResolveShellID(args)
	}
	if err := t.SM.Interrupt(a.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("interrupt sent to %s", a.ID), nil
}

// ListShellsTool lists all shell sessions.
type ListShellsTool struct {
	SM *ShellManager
}

func (t ListShellsTool) Name() string        { return "list_shells" }
func (t ListShellsTool) Description() string { return "List all persistent shell sessions." }
func (t ListShellsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}
func (t ListShellsTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	shells := t.SM.List()
	if len(shells) == 0 {
		return "no active shell sessions", nil
	}
	var sb strings.Builder
	for _, s := range shells {
		status := "open"
		switch s.Status() {
		case ShellStatusExited:
			status = "exited"
		case ShellStatusClosed:
			status = "closed"
		}
		label := s.Label()
		if label == "" {
			label = s.ID()
		} else {
			label = fmt.Sprintf("%s (%s)", s.ID(), label)
		}
		lost := ""
		if dropped := s.LostBytes(); dropped > 0 {
			lost = fmt.Sprintf("  dropped=%d", dropped)
		}
		fmt.Fprintf(&sb, "%s  kind=%s  status=%s  bytes=%d%s\n", label, s.Kind(), status, s.TotalBytes(), lost)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

type idArgs struct {
	ID         string   `json:"id"`
	ID2        string   `json:"ID"`
	ShellID    string   `json:"shell_id"`
	ShellID2   string   `json:"shellID"`
	ShellID3   string   `json:"ShellID"`
	SessionID  string   `json:"session_id"`
	SessionID2 string   `json:"sessionID"`
	SessionID3 string   `json:"SessionID"`
	Session    string   `json:"session"`
	Session2   string   `json:"Session"`
}

func ResolveShellID(args json.RawMessage) string {
	var p idArgs
	if err := json.Unmarshal(args, &p); err == nil {
		if p.ID != "" {
			return p.ID
		}
		if p.ID2 != "" {
			return p.ID2
		}
		if p.ShellID != "" {
			return p.ShellID
		}
		if p.ShellID2 != "" {
			return p.ShellID2
		}
		if p.ShellID3 != "" {
			return p.ShellID3
		}
		if p.SessionID != "" {
			return p.SessionID
		}
		if p.SessionID2 != "" {
			return p.SessionID2
		}
		if p.SessionID3 != "" {
			return p.SessionID3
		}
		if p.Session != "" {
			return p.Session
		}
		if p.Session2 != "" {
			return p.Session2
		}
	}
	return ""
}
