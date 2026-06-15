package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bouwerp/ageni/internal/llm"
)

var toolAliases = map[string]string{
	"shell":       "run_bash",
	"bash":        "run_bash",
	"cmd":         "run_bash",
	"run":         "run_bash",
	"run_command": "run_bash",
	"mkdir":       "make_dir",
	"rm":          "delete_file",
	"delete":      "delete_file",
	"remove":      "delete_file",
	"mv":          "move_file",
	"move":        "move_file",
	"rename":      "move_file",
	"cat":         "read_file",
	"head":        "read_file",
	"tail":        "read_file",
	"search":      "grep",
	"grep_search": "grep",
	"rg":          "grep",
	"ripgrep":     "grep",
	"find":        "glob",
	"edit":        "apply_diff",
	"patch":       "apply_diff",
	"apply_patch": "apply_diff",
	"ls":          "list_dir",
	"dir":         "list_dir",
	"list":        "list_dir",
}

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

// OutputScrubber is a function applied to every tool result before it is
// returned to the LLM. It is used to remove secret values from output,
// replacing them with a [REDACTED:alias] placeholder.
type OutputScrubber func(string) string

// Registry holds tools and produces deterministic ToolDef lists for the LLM.
type Registry struct {
	tools    map[string]Tool
	scrubber OutputScrubber
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// SetScrubber attaches an OutputScrubber to the registry. Every result
// returned by Execute (both success and error) is passed through the scrubber
// before being given to the LLM. This is a backstop against accidental secret
// leakage through tool output.
func (r *Registry) SetScrubber(s OutputScrubber) { r.scrubber = s }

func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Missing returns the requested tool names that are not present in the
// registry after normalizing tool-name suffix noise.
func (r *Registry) Missing(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	missing := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := sanitizeToolName(strings.TrimSpace(raw))
		if name == "" {
			name = strings.TrimSpace(raw)
		}
		if name == "" {
			continue
		}
		if _, ok := r.tools[name]; ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing
}

// Names returns a sorted list of all tool names in the registry.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	name := sanitizeToolName(call.Name)
	if target, ok := toolAliases[strings.ToLower(name)]; ok {
		if _, exists := r.tools[target]; exists {
			name = target
		}
	}
	t, ok := r.tools[name]
	if !ok {
		return llm.ToolResult{
			ToolCallID: call.ID,
			Content:    r.unknownToolMessage(name),
			IsError:    true,
		}
	}
	call.Name = name
	out, err := t.Call(ctx, call.Arguments)
	if err != nil {
		// Context cancellation is intentional (user pressed Esc or the
		// sub-agent was killed). Don't surface it as a tool error — the
		// caller's loop will detect ctx.Err() on the next iteration and
		// exit cleanly. Return a neutral [cancelled] result so message
		// history stays well-formed without confusing the model.
		if errors.Is(err, context.Canceled) {
			return llm.ToolResult{ToolCallID: call.ID, Content: "[cancelled]"}
		}
		msg := "Error: " + sanitizeOutput(err.Error())
		if r.scrubber != nil {
			msg = r.scrubber(msg)
		}
		return llm.ToolResult{
			ToolCallID: call.ID,
			Content:    msg,
			IsError:    true,
		}
	}
	content := sanitizeOutput(out)
	if r.scrubber != nil {
		content = r.scrubber(content)
	}
	return llm.ToolResult{ToolCallID: call.ID, Content: content}
}

// sanitizeToolName strips any suffix that begins with a character not valid in
// a tool name (alphanumeric, underscore, hyphen). Some LLMs inject special
// tokens like "<|channel|>" into tool names; stripping them lets us match the
// intended tool instead of returning a spurious unknown-tool error.
func sanitizeToolName(name string) string {
	for i, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-') {
			return name[:i]
		}
	}
	return name
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
	nameLower := strings.ToLower(name)
	if nameLower == "tree" || nameLower == "print_tree" {
		return "Did you mean list_dir or glob? glob supports recursive ** patterns."
	}
	if nameLower == "cp" || nameLower == "copy" {
		return `Did you mean run_bash with "cp <src> <dst>"?`
	}
	target, ok := toolAliases[nameLower]
	if !ok {
		return ""
	}
	switch target {
	case "make_dir":
		return `Did you mean make_dir? Or run_bash with "mkdir -p <path>".`
	case "list_dir":
		return "Did you mean list_dir or glob? glob supports recursive ** patterns."
	case "delete_file":
		return "Did you mean delete_file?"
	case "move_file":
		return "Did you mean move_file?"
	case "read_file":
		return "Did you mean read_file? It supports offset+limit for line ranges."
	case "grep":
		return "Did you mean grep? It uses ripgrep with --json output."
	case "glob":
		return "Did you mean glob? Or run_bash with find."
	case "apply_diff":
		return "Did you mean apply_diff (SEARCH/REPLACE blocks or whole-file), edit_file (single replacement), or multi_edit (atomic batch)?"
	case "run_bash":
		return "Did you mean run_bash (for single commands)? Or if using persistent/stateful terminal sessions, use open_shell + shell_exec."
	}
	return "Did you mean " + target + "?"
}

// Subset returns a new Registry containing only the named tools. Used to
// scope what a sub-agent is allowed to call.
func (r *Registry) Subset(names []string) *Registry {
	sub := NewRegistry()
	sub.scrubber = r.scrubber
	for _, n := range names {
		if t, ok := r.tools[n]; ok {
			sub.Register(t)
		}
	}
	return sub
}
