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
	summary := summarizeToolArgs(args)
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

// summarizeToolArgs produces a compact one-line summary of a tool call's
// arguments. Common single-key tools (run_bash, read_file, grep, web_fetch
// etc.) get a shortcut where just the key value is shown; otherwise we list
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
