package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// styledLines applies style to each non-empty line of s independently,
// keeping the ANSI reset on the same line as its content. This prevents
// the reset code from landing after a newline where viewport truncation or
// scrolling strips it and bleeds colour into adjacent lines.
func styledLines(style lipgloss.Style, s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = style.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

const diffMaxLines = 50

var (
	diffAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // green
	diffDelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // red
	diffHunkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Faint(true)
	diffHeaderStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	diffCtxStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	diffTruncStyle  = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
)

// renderDiff formats a unified-diff string into a colourised, line-limited
// block suitable for the chat pane. maxLines caps the number of diff lines
// shown (not counting the file header); remaining lines are replaced with a
// truncation notice. Pass diffMaxLines as the default.
func renderDiff(diff string, maxLines int) string {
	if diff == "" {
		return ""
	}

	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")

	// Extract the file path from the first "--- " or "+++ " header line.
	path := ""
	body := lines
	for i, l := range lines {
		if strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") {
			// Prefer the +++ line (new name); strip leading "b/" if present.
			candidate := strings.TrimPrefix(strings.TrimPrefix(l[4:], "b/"), "a/")
			// Both --- and +++ have the same label in our usage; just grab the first one.
			if path == "" {
				path = candidate
			}
			body = lines[i+1:]
			continue
		}
		if path != "" {
			body = lines[i:]
			break
		}
	}
	// body now starts from the first @@ hunk or content line.

	var sb strings.Builder
	if path != "" {
		sb.WriteString(diffHeaderStyle.Render("  "+path) + "\n")
	}

	shown := 0
	total := len(body)
	for i, l := range body {
		if shown >= maxLines {
			remaining := total - i
			sb.WriteString(diffTruncStyle.Render(fmt.Sprintf("  … %d more line(s) not shown", remaining)))
			break
		}
		switch {
		case strings.HasPrefix(l, "+"):
			sb.WriteString(diffAddStyle.Render(l))
		case strings.HasPrefix(l, "-"):
			sb.WriteString(diffDelStyle.Render(l))
		case strings.HasPrefix(l, "@@"):
			sb.WriteString(diffHunkStyle.Render(l))
		default:
			sb.WriteString(diffCtxStyle.Render(l))
		}
		sb.WriteByte('\n')
		shown++
	}
	return strings.TrimRight(sb.String(), "\n")
}
