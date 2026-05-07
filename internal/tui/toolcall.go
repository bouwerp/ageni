package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/bouwerp/ageni/internal/llm"
)

var (
	toolPrefixStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	toolNameStyle   = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	toolArgsStyle   = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	toolOKStyle     = lipgloss.NewStyle().Foreground(colorOK)
	toolErrStyle    = lipgloss.NewStyle().Foreground(colorErr).Bold(true)
	toolResultStyle = lipgloss.NewStyle().Foreground(colorMuted)
)

// renderToolCall returns a single styled line representing a tool invocation.
// Format: "▸ tool_name  arg-summary" with the tool name in accent + bold and
// the arg summary in muted italic.
func renderToolCall(name string, args json.RawMessage) string {
	summary := summarizeToolCallArgs(name, args)
	left := toolPrefixStyle.Render("▸ ") + toolNameStyle.Render(name)
	if summary == "" {
		return left
	}
	return left + "  " + toolArgsStyle.Render(summary)
}

// renderToolResult returns a styled, indented result line. Errors stand out
// in red bold; success results render as a one-line muted snippet preceded
// by ✓.
func renderToolResult(result *llm.ToolResult) string {
	if result == nil {
		return ""
	}
	snip := compactSnippetTUI(result.Content, 160)
	if result.IsError {
		return toolErrStyle.Render("  ✗ ") + toolErrStyle.Render(snip)
	}
	if snip == "" {
		return toolOKStyle.Render("  ✓ done")
	}
	return toolOKStyle.Render("  ✓ ") + toolResultStyle.Render(snip)
}

// summarizeToolCallArgs produces a compact one-line summary of a tool call's
// arguments, with tool-specific formatting for common operations.
func summarizeToolCallArgs(toolName string, args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return clip(string(args), 120)
	}
	if len(m) == 0 {
		return ""
	}

	str := func(key string) string {
		if v, ok := m[key]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}

	switch toolName {
	case "grep", "find_in_codebase":
		pattern := str("pattern")
		if pattern == "" {
			pattern = str("query")
		}
		path := str("path")
		if path == "" {
			path = str("file_pattern")
		}
		if pattern != "" && path != "" {
			return fmt.Sprintf("%q  in %s", clip(pattern, 60), clip(path, 60))
		}
		if pattern != "" {
			return fmt.Sprintf("%q", clip(pattern, 100))
		}

	case "edit_file", "str_replace", "str_replace_editor":
		path := str("path")
		oldStr := str("old_str")
		newStr := str("new_str")
		if path != "" && oldStr != "" {
			oldSnip := clipLine(oldStr, 40)
			newSnip := clipLine(newStr, 40)
			return fmt.Sprintf("%s  %q → %q", path, oldSnip, newSnip)
		}
		if path != "" {
			return path
		}

	case "write_file":
		path := str("path")
		if path != "" {
			content := str("content")
			lines := strings.Count(content, "\n") + 1
			if content == "" {
				lines = 0
			}
			return fmt.Sprintf("%s  (%d lines)", path, lines)
		}

	case "multi_edit":
		path := str("path")
		if edits, ok := m["edits"]; ok {
			if arr, ok := edits.([]any); ok {
				if path != "" {
					return fmt.Sprintf("%s  %d edit(s)", path, len(arr))
				}
				return fmt.Sprintf("%d edit(s)", len(arr))
			}
		}
		if path != "" {
			return path
		}

	case "glob":
		pattern := str("pattern")
		path := str("path")
		if pattern != "" && path != "" {
			return fmt.Sprintf("%q  in %s", clip(pattern, 60), clip(path, 60))
		}
		if pattern != "" {
			return fmt.Sprintf("%q", clip(pattern, 100))
		}

	case "read_file":
		path := str("path")
		startLine := str("start_line")
		endLine := str("end_line")
		if path != "" && (startLine != "" || endLine != "") {
			return fmt.Sprintf("%s  :%s–%s", path, startLine, endLine)
		}
		if path != "" {
			return path
		}

	case "run_bash":
		if cmd := str("command"); cmd != "" {
			return clip(cmd, 120)
		}

	case "spawn_subagent":
		objective := str("objective")
		tier := str("model_tier")
		if objective != "" && tier != "" {
			return fmt.Sprintf("[%s] %s", tier, clip(objective, 80))
		}
		if objective != "" {
			return clip(objective, 100)
		}
	}

	// Generic fallback — try common single-key shortcuts.
	return summarizeToolArgs(args)
}

// clipLine clips a possibly multi-line string to a single line then clips length.
func clipLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx] + "…"
	}
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// summarizeToolArgs produces a compact one-line summary of a tool call's
// arguments. Common single-key tools get a shortcut; otherwise we list
// up to two key=value pairs.
func summarizeToolArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return clip(string(args), 120)
	}
	if len(m) == 0 {
		return ""
	}

	// Hot-path single-key shortcuts so the common cases read naturally.
	for _, k := range []string{"command", "path", "pattern", "url", "query", "name"} {
		if v, ok := m[k]; ok {
			s := fmt.Sprintf("%v", v)
			if len(m) == 1 {
				return clip(s, 120)
			}
			return clip(s, 100) + extraKeysSuffix(m, k)
		}
	}

	// Generic fallback: sorted key=value, capped.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for i, k := range keys {
		if i >= 2 {
			parts = append(parts, fmt.Sprintf("…+%d", len(keys)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, clip(fmt.Sprintf("%v", m[k]), 60)))
	}
	return strings.Join(parts, ", ")
}

func extraKeysSuffix(m map[string]any, primary string) string {
	if len(m) <= 1 {
		return ""
	}
	other := make([]string, 0, len(m)-1)
	for k := range m {
		if k == primary {
			continue
		}
		other = append(other, k)
	}
	sort.Strings(other)
	if len(other) > 3 {
		other = append(other[:3], "…")
	}
	return "  " + lipgloss.NewStyle().Faint(true).Render("("+strings.Join(other, ", ")+")")
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// compactSnippetTUI is the TUI-layer counterpart of tools.compactSnippet for
// rendering tool result snippets in chat — collapses whitespace + truncates.
func compactSnippetTUI(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
