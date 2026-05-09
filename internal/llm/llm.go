package llm

import (
	"context"
	"encoding/json"
)

// Role identifies who authored a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn in a conversation. Either Text or one or more ToolCalls
// (assistant) or ToolResults (tool role) are populated.
type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
	// ReasoningContent holds the chain-of-thought produced by reasoning models
	// (e.g. DeepSeek thinking mode). It must be echoed back in the next request
	// so the provider can continue the reasoning trace.
	ReasoningContent string
}

// ToolCall is an assistant-issued tool invocation.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResult is a tool's response to a ToolCall.
type ToolResult struct {
	ToolCallID string
	Content    string
	IsError    bool
}

// ToolDef describes a tool exposed to the model.
type ToolDef struct {
	Name        string
	Description string
	// JSON schema as raw JSON.
	Schema json.RawMessage
}

// Request is a single inference request.
type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []ToolDef

	// Temperature, MaxTokens, etc. Optional.
	Temperature *float64
	MaxTokens   int
}

// StreamEvent is one event from a streamed response.
type StreamEventType string

const (
	StreamEventText     StreamEventType = "text"
	StreamEventToolCall StreamEventType = "tool_call"
	StreamEventDone     StreamEventType = "done"
	StreamEventError    StreamEventType = "error"
	StreamEventThinking StreamEventType = "thinking"
)

type StreamEvent struct {
	Type StreamEventType

	TextDelta        string
	ReasoningContent string // set on StreamEventDone when the model produced reasoning (e.g. DeepSeek)
	ToolCall         *ToolCall

	Usage *Usage
	Err   error
}

// Usage captures token counts for one response.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// Adapter is the provider-agnostic interface for LLM inference.
type Adapter interface {
	// Stream runs an inference request and emits events on the returned channel.
	// The channel is closed when the response completes (StreamEventDone) or errors.
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)

	// Provider returns a short identifier for telemetry/logging.
	Provider() string
}
