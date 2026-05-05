package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
// Unknown-tool errors include the full available-tool list and a hint to use
// run_bash for arbitrary shell commands, so the model can self-correct on
// the next turn instead of repeating the same mistake.
func (r *Registry) Execute(ctx context.Context, call llm.ToolCall) llm.ToolResult {
	t, ok := r.tools[call.Name]
	if !ok {
		return llm.ToolResult{
			ToolCallID: call.ID,
			Content:    r.unknownToolMessage(call.Name),
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

func (r *Registry) unknownToolMessage(name string) string {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	fmt.Fprintf(&sb, "unknown tool: %q.\n\n", name)
	if hint := guessAlternative(name); hint != "" {
		sb.WriteString(hint + "\n\n")
	}
	sb.WriteString("Available tools:\n")
	for _, n := range names {
		sb.WriteString("  - " + n + "\n")
	}
	sb.WriteString("\nFor any shell command not in this list (e.g. mv, cp, rm, find, tree), use run_bash.")
	return sb.String()
}

// guessAlternative maps common hallucinated tool names to the correct one.
func guessAlternative(name string) string {
	switch strings.ToLower(name) {
	case "mkdir":
		return `Did you mean make_dir? Or run_bash with "mkdir -p <path>".`
	case "ls", "dir", "tree", "print_tree", "list":
		return `Did you mean list_dir or glob? glob supports recursive ** patterns.`
	case "rm", "delete", "remove", "unlink":
		return "Did you mean delete_file?"
	case "mv", "move", "rename":
		return "Did you mean move_file?"
	case "cp", "copy":
		return `Did you mean run_bash with "cp <src> <dst>"?`
	case "cat", "head", "tail":
		return "Did you mean read_file? It supports offset+limit for line ranges."
	case "grep", "search", "rg", "ripgrep":
		return "Did you mean grep? It uses ripgrep with --json output."
	case "find":
		return "Did you mean glob? Or run_bash with find."
	case "edit", "patch", "apply_patch":
		return "Did you mean edit_file (single replacement) or multi_edit (atomic batch)?"
	}
	return ""
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
