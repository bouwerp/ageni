package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/bouwerp/ageni/internal/llm"
)

// Tool is a typed unit of work the model can invoke.
type Tool interface {
	Name() string
	Description() string
	// Schema returns the JSON-schema for this tool's arguments. Should be
	// stable across calls (same byte sequence) for prompt caching.
	Schema() json.RawMessage
	// Call executes the tool with raw JSON arguments and returns its output.
	// The returned string is fed back to the model as the tool result.
	Call(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds tools and produces deterministic ToolDef lists for the LLM.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Definitions returns ToolDefs sorted by name (deterministic for cache hits).
func (r *Registry) Definitions() []llm.ToolDef {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	defs := make([]llm.ToolDef, 0, len(names))
	for _, n := range names {
		t := r.tools[n]
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return defs
}

// Execute runs a tool by name with the given JSON args. Returns a ToolResult.
func (r *Registry) Execute(ctx context.Context, call llm.ToolCall) llm.ToolResult {
	t, ok := r.tools[call.Name]
	if !ok {
		return llm.ToolResult{
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("unknown tool: %s", call.Name),
			IsError:    true,
		}
	}
	out, err := t.Call(ctx, call.Arguments)
	if err != nil {
		return llm.ToolResult{
			ToolCallID: call.ID,
			Content:    err.Error(),
			IsError:    true,
		}
	}
	return llm.ToolResult{ToolCallID: call.ID, Content: out}
}

// Subset returns a new Registry containing only the named tools. Used to
// scope what a sub-agent is allowed to call.
func (r *Registry) Subset(names []string) *Registry {
	sub := NewRegistry()
	for _, n := range names {
		if t, ok := r.tools[n]; ok {
			sub.Register(t)
		}
	}
	return sub
}
