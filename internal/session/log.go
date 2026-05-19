package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bouwerp/ageni/internal/agent"
	"github.com/bouwerp/ageni/internal/homedir"
)

// Logger appends every Bus event to a JSONL file in ~/.ageni/sessions/.
type Logger struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
	path string
}

// NewLogger creates a JSONL log file inside the given session's directory.
// Pass nil to fall back to the legacy ~/.ageni/sessions/<timestamp>.jsonl
// shape — only used by code paths that haven't been ported yet.
func NewLogger(s *Session) (*Logger, error) {
	var path string
	if s != nil {
		path = s.Path("log.jsonl")
	} else {
		home, err := homedir.Dir()
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(home, ".ageni", "sessions")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		path = filepath.Join(dir, fmt.Sprintf("%s.jsonl", time.Now().Format("20060102-150405")))
	}
	// Append, don't truncate — on resume, prior turns must remain in the
	// log so future replays can reconstruct them.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec
	if err != nil {
		return nil, err
	}
	return &Logger{file: f, enc: json.NewEncoder(f), path: path}, nil
}

func (l *Logger) Path() string { return l.path }

func (l *Logger) Close() error { return l.file.Close() }

// Run consumes events from a subscription and writes them to disk until ctx is done.
func (l *Logger) Run(ctx context.Context, sub <-chan agent.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			l.WriteEvent(ev)
		}
	}
}

type entry struct {
	Kind          string `json:"kind"`
	At            string `json:"at"`
	CorrelationID string `json:"correlation_id,omitempty"`
	SubagentID    string `json:"subagent_id,omitempty"`
	Text          string `json:"text,omitempty"`
	ToolName      string `json:"tool_name,omitempty"`
	ToolArgs      string `json:"tool_args,omitempty"`
	ToolResult    string `json:"tool_result,omitempty"`
	ToolError     bool   `json:"tool_error,omitempty"`
	ShellKind     string `json:"shell_kind,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	InputTokens   int    `json:"input_tokens,omitempty"`
	OutputTokens  int    `json:"output_tokens,omitempty"`
	CacheRead     int    `json:"cache_read_tokens,omitempty"`
	CacheCreation int    `json:"cache_creation_tokens,omitempty"`
	SubagentTask  string `json:"subagent_task,omitempty"`
	SubagentModel string `json:"subagent_model,omitempty"`
	Err           string `json:"err,omitempty"`
}

// WriteEvent persists one event to the append-only session journal.
func (l *Logger) WriteEvent(ev agent.Event) {
	e := entry{
		Kind:          string(ev.Kind),
		At:            ev.At.Format(time.RFC3339Nano),
		CorrelationID: ev.CorrelationID,
		SubagentID:    ev.SubagentID,
		Text:          ev.Text,
		SubagentTask:  ev.SubagentTask,
		SubagentModel: ev.SubagentModel,
		ShellKind:     string(ev.ShellKind),
		Bytes:         ev.Bytes,
	}
	if ev.ToolCall != nil {
		e.ToolName = ev.ToolCall.Name
		e.ToolArgs = string(ev.ToolCall.Arguments)
	}
	if ev.ToolResult != nil {
		e.ToolResult = ev.ToolResult.Content
		e.ToolError = ev.ToolResult.IsError
	}
	if ev.Usage != nil {
		e.InputTokens = ev.Usage.InputTokens
		e.OutputTokens = ev.Usage.OutputTokens
		e.CacheRead = ev.Usage.CacheReadTokens
		e.CacheCreation = ev.Usage.CacheCreationTokens
	}
	if ev.Err != nil {
		e.Err = ev.Err.Error()
	}
	l.mu.Lock()
	_ = l.enc.Encode(&e)
	_ = l.file.Sync()
	l.mu.Unlock()
}
