package tui

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// atComplete holds the live state of the @ path autocomplete dropdown.
type atComplete struct {
	active  bool
	query   string   // text typed after @, used to filter
	atByte  int      // byte offset of the '@' in the full input string
	matches []string // ranked relative paths
	sel     int      // currently highlighted index (0-based)
}

// atFileCache is a simple one-slot cache. It's rebuilt whenever cwd changes.
var atFileCache struct {
	cwd   string
	files []string
}

// listFiles walks cwd and returns relative paths for all regular files,
// skipping common noise dirs. Capped at 2000 entries.
func listFiles(cwd string) []string {
	if atFileCache.cwd == cwd && len(atFileCache.files) > 0 {
		return atFileCache.files
	}
	var files []string
	_ = filepath.WalkDir(cwd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			skip := name == ".git" || name == "node_modules" || name == "vendor" ||
				name == ".hg" || name == "__pycache__" || name == "dist" ||
				name == "build" || name == ".next" || name == "target" ||
				(strings.HasPrefix(name, ".") && name != ".")
			if skip {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(cwd, path)
		if relErr != nil {
			return nil
		}
		files = append(files, rel)
		if len(files) >= 2000 {
			return filepath.SkipAll
		}
		return nil
	})
	atFileCache.cwd = cwd
	atFileCache.files = files
	return files
}

// scoreMatch returns a [0,100] relevance score for path vs. query. Returns 0
// when the path has no relation to the query (safe to filter out).
func scoreMatch(path, query string) int {
	if query == "" {
		return 1 // empty query shows everything
	}
	ql := strings.ToLower(query)
	pl := strings.ToLower(path)
	base := strings.ToLower(filepath.Base(path))

	switch {
	case base == ql:
		return 100
	case strings.HasPrefix(base, ql):
		return 90
	case strings.Contains(base, ql):
		return 80
	case strings.Contains(pl, ql):
		return 70
	}
	// Per-segment prefix match (e.g. "tui" matches "internal/tui/")
	for _, seg := range strings.Split(pl, "/") {
		if strings.HasPrefix(seg, ql) {
			return 60
		}
	}
	// Subsequence match: do all query chars appear in order in path?
	qi := 0
	for _, c := range pl {
		if qi < len(ql) && c == rune(ql[qi]) {
			qi++
		}
	}
	if qi == len(ql) {
		return 10 + (len(ql)*30)/max(len(pl), 1)
	}
	return 0
}

// matchFiles returns up to limit paths from cwd ranked by how well they match
// query, using a scoring scheme similar to VSCode's fuzzy file picker.
func matchFiles(cwd, query string, limit int) []string {
	files := listFiles(cwd)
	type scored struct {
		path  string
		score int
	}
	results := make([]scored, 0, len(files))
	for _, f := range files {
		if s := scoreMatch(f, query); s > 0 {
			results = append(results, scored{f, s})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return len(results[i].path) < len(results[j].path)
	})
	out := make([]string, 0, min(limit, len(results)))
	for i := range min(limit, len(results)) {
		out = append(out, results[i].path)
	}
	return out
}

// detectAtPrefix walks backward from the end of textUpToCursor looking for an
// @<word> token where <word> has no whitespace. Returns (byteOffset, query,
// true) on success. The byteOffset is the byte index of '@' in the text.
//
// Only triggers when '@' is immediately preceded by whitespace or is at the
// very start of the input, so email@example.com-style tokens are ignored.
func detectAtPrefix(textUpToCursor string) (atByte int, query string, ok bool) {
	r := []rune(textUpToCursor)
	for i := len(r) - 1; i >= 0; i-- {
		if unicode.IsSpace(r[i]) {
			break // no @ found before whitespace
		}
		if r[i] == '@' {
			if i == 0 || unicode.IsSpace(r[i-1]) {
				q := string(r[i+1:])
				if !strings.ContainsAny(q, " \t\n") {
					return len([]byte(string(r[:i]))), q, true
				}
			}
			break // @ embedded in a word — stop
		}
	}
	return 0, "", false
}

// inputTextUpToCursor reconstructs the portion of the textarea value that
// precedes the cursor, using Line() and LineInfo() from the textarea model.
func inputTextUpToCursor(value string, row, startCol, colOffset int) string {
	lines := strings.Split(value, "\n")
	if row >= len(lines) {
		return value
	}
	var sb strings.Builder
	for i := range row {
		sb.WriteString(lines[i])
		sb.WriteByte('\n')
	}
	runes := []rune(lines[row])
	col := min(startCol+colOffset, len(runes))
	sb.WriteString(string(runes[:col]))
	return sb.String()
}

// ── rendering ──────────────────────────────────────────────────────────────

const atCompleteMaxItems = 8

var (
	atItemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	atSelStyle      = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("231")).Bold(true)
	atContainerStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorderHi).
				Padding(0, 1)
)

// render draws the suggestion list. width is the available terminal width so
// the box can be sized appropriately.
func (ac *atComplete) render(width int) string {
	if !ac.active || len(ac.matches) == 0 {
		return ""
	}
	innerW := width - 6 // account for border (2) + padding (2 each side) + gutter
	if innerW < 20 {
		innerW = 20
	}

	var sb strings.Builder
	for i, m := range ac.matches {
		// Truncate long paths from the left so the filename is always visible.
		display := m
		if len(display) > innerW {
			display = "…" + display[len(display)-innerW+1:]
		}
		// Pad to fixed width.
		padded := display + strings.Repeat(" ", max(0, innerW-lipgloss.Width(display)))
		if i == ac.sel {
			sb.WriteString(atSelStyle.Render("▶ "+padded))
		} else {
			sb.WriteString(atItemStyle.Render("  "+padded))
		}
		if i < len(ac.matches)-1 {
			sb.WriteByte('\n')
		}
	}
	hint := mutedStyle.Render("  ↑↓ select · Tab/Enter insert · Esc dismiss")
	return atContainerStyle.Render(sb.String()) + "\n" + hint
}

// height returns the number of terminal lines that render() will occupy.
func (ac *atComplete) height() int {
	if !ac.active || len(ac.matches) == 0 {
		return 0
	}
	// border top/bottom (2) + items + hint line (1)
	return len(ac.matches) + 2 + 1
}

// selectedPath returns the currently highlighted file path, or "" if nothing
// is selected.
func (ac *atComplete) selectedPath() string {
	if ac.sel >= 0 && ac.sel < len(ac.matches) {
		return ac.matches[ac.sel]
	}
	return ""
}

// cwd returns the working directory, cached via os.Getwd.
func getCwd() string {
	cwd, _ := os.Getwd()
	return cwd
}
