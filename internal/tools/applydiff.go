package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/ageni/internal/lsp"
)

// ApplyDiff applies edits to a file using an explicit edit format. More
// reliable than edit_file for multi-block changes and gives precise
// miss-diagnostics so the model can self-correct without re-reading the
// file.
//
// Two formats are supported:
//
//   - search_replace (default): Aider-style SEARCH/REPLACE blocks
//     <<<<<<< SEARCH
//     ...content to find...
//     =======
//     ...replacement...
//     >>>>>>> REPLACE
//     Multiple blocks per call applied in order.
//
//   - whole: content replaces the entire file (creates if missing).
//
// On a SEARCH miss, the tool returns the closest candidate region in
// the file (line numbers + content), letting the model retry with the
// corrected SEARCH text.
type ApplyDiff struct{ Tracker *ChangeTracker }

func (ApplyDiff) Name() string { return "apply_diff" }

func (ApplyDiff) Description() string {
	return `Apply edits to a file using an explicit edit format. More reliable than edit_file for multi-block changes; on a search miss it returns the closest candidate region so you can correct without re-reading the file.

Formats:
- search_replace (default — recommended for Claude / Anthropic models):
  one or more SEARCH/REPLACE blocks in Aider's format:

      <<<<<<< SEARCH
      ...exact content to find...
      =======
      ...replacement content...
      >>>>>>> REPLACE

  Multiple blocks in the same content string are applied in order. Each SEARCH must match exactly once in the current file state unless replace_all is set. Surrounding context lines in SEARCH must match verbatim — whitespace counts.

- whole: content replaces the entire file. Creates the file if missing. Equivalent to write_file but kept here so editors can pick one tool.

When a SEARCH block doesn't match, the tool returns up to 3 closest candidate regions (with line numbers) so you can fix the search text and retry. Don't re-read the file just to retry — work from the candidate output.`
}

func (ApplyDiff) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string"},
  "format":{"type":"string","enum":["search_replace","whole"],"description":"Edit format. Default search_replace."},
  "content":{"type":"string","description":"For search_replace: one or more <<<<<<< SEARCH … ======= … >>>>>>> REPLACE blocks. For whole: the new full file content."},
  "replace_all":{"type":"boolean","description":"For search_replace: when true each SEARCH may match multiple times and all are replaced. Default false."}
},
"required":["path","content"]
}`)
}

type applyDiffArgs struct {
	Path       string `json:"path"`
	Format     string `json:"format"`
	Content    string `json:"content"`
	ReplaceAll bool   `json:"replace_all"`
}

func (a ApplyDiff) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p applyDiffArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		p.Path = ResolvePath(args)
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	validatedPath, err := ValidatePath(p.Path)
	if err != nil {
		return "", err
	}
	p.Path = validatedPath
	if p.Content == "" {
		p.Content = ResolveContent(args)
	}
	if p.Content == "" {
		return "", errors.New("content is required")
	}
	if p.Format == "" {
		p.Format = "search_replace"
	}

	GlobalLockManager.Lock(p.Path)
	defer GlobalLockManager.Unlock(p.Path)

	abs, _ := filepath.Abs(p.Path)

	switch p.Format {
	case "whole":
		existed := false
		if _, err := os.Stat(abs); err == nil {
			existed = true
		}
		step := a.Tracker.BeginMutation(abs)
		if dir := filepath.Dir(p.Path); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
		if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
			return "", err
		}
		_ = lsp.GlobalLSPManager.UpdateFile(ctx, p.Path, p.Content)
		kind := ChangeCreated
		if existed {
			kind = ChangeEdited
		}
		a.Tracker.Record(Change{Path: abs, Kind: kind, Step: step})
		result := fmt.Sprintf("wrote %d bytes to %s (whole)", len(p.Content), p.Path)
		if lint := lintAfterEdit(abs); lint != "" {
			result += "\n" + lint
		}
		return result, nil

	case "search_replace":
		body, err := os.ReadFile(p.Path) //nolint:gosec
		if err != nil {
			if os.IsNotExist(err) && !strings.Contains(p.Content, "<<<<<<< SEARCH") {
				step := a.Tracker.BeginMutation(abs)
				if dir := filepath.Dir(p.Path); dir != "" {
					_ = os.MkdirAll(dir, 0o755)
				}
				if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
					return "", err
				}
				_ = lsp.GlobalLSPManager.UpdateFile(ctx, p.Path, p.Content)
				a.Tracker.Record(Change{Path: abs, Kind: ChangeCreated, Step: step})
				result := fmt.Sprintf("wrote %d bytes to %s (whole - auto-created non-existent file)", len(p.Content), p.Path)
				if lint := lintAfterEdit(abs); lint != "" {
					result += "\n" + lint
				}
				return result, nil
			}
			return "", fmt.Errorf("read %s: %w", p.Path, err)
		}
		blocks, err := parseSearchReplaceBlocks(p.Content)
		if err != nil {
			return "", err
		}
		if len(blocks) == 0 {
			return "", errors.New("no SEARCH/REPLACE blocks found in content (expected <<<<<<< SEARCH … ======= … >>>>>>> REPLACE)")
		}
		text := string(body)
		applied := 0
		for i, b := range blocks {
			if b.Search == "" {
				return "", fmt.Errorf("block %d: empty SEARCH", i+1)
			}
			count := strings.Count(text, b.Search)
			switch {
			case count == 0:
				return "", missDiagnostic(text, b.Search, i+1)
			case p.ReplaceAll:
				text = strings.ReplaceAll(text, b.Search, b.Replace)
			case count == 1:
				text = strings.Replace(text, b.Search, b.Replace, 1)
			default:
				return "", fmt.Errorf("block %d: SEARCH matches %d times in %s; pass replace_all=true to replace all, or add more context to make the SEARCH unique", i+1, count, p.Path)
			}
			applied++
		}
		step := a.Tracker.BeginMutation(abs)
		if err := os.WriteFile(p.Path, []byte(text), 0o644); err != nil { //nolint:gosec
			return "", err
		}
		_ = lsp.GlobalLSPManager.UpdateFile(ctx, p.Path, text)
		a.Tracker.Record(Change{Path: abs, Kind: ChangeEdited, Step: step})
		result := fmt.Sprintf("applied %d block(s) to %s (search_replace)", applied, p.Path)
		if lint := lintAfterEdit(abs); lint != "" {
			result += "\n" + lint
		}
		return result, nil

	default:
		return "", fmt.Errorf("unknown format %q (supported: search_replace, whole)", p.Format)
	}
}

// searchReplaceBlock is one parsed SEARCH/REPLACE pair.
type searchReplaceBlock struct {
	Search  string
	Replace string
}

// parseSearchReplaceBlocks parses Aider-style blocks. The format is
// strict: each block must have all three markers in order, and the
// closing >>>>>>> REPLACE delimits the block. Content between blocks
// is ignored (so the model can interleave commentary if it wants).
func parseSearchReplaceBlocks(s string) ([]searchReplaceBlock, error) {
	const (
		searchMark = "<<<<<<< SEARCH"
		divider    = "======="
		replaceEnd = ">>>>>>> REPLACE"
	)
	var blocks []searchReplaceBlock
	rest := s
	for {
		i := strings.Index(rest, searchMark)
		if i < 0 {
			break
		}
		rest = rest[i+len(searchMark):]
		// Strip the newline immediately after the marker if present.
		rest = strings.TrimPrefix(rest, "\n")
		// Find divider on its own line.
		divIdx := indexOfLine(rest, divider)
		if divIdx < 0 {
			return nil, errors.New("malformed block: SEARCH not closed by ======= divider")
		}
		search := rest[:divIdx]
		// Strip the trailing newline before the divider so SEARCH content
		// ends exactly at the user's last line.
		search = strings.TrimSuffix(search, "\n")
		rest = rest[divIdx+len(divider):]
		rest = strings.TrimPrefix(rest, "\n")
		endIdx := indexOfLine(rest, replaceEnd)
		if endIdx < 0 {
			return nil, errors.New("malformed block: ======= not closed by >>>>>>> REPLACE")
		}
		replace := rest[:endIdx]
		replace = strings.TrimSuffix(replace, "\n")
		rest = rest[endIdx+len(replaceEnd):]
		blocks = append(blocks, searchReplaceBlock{Search: search, Replace: replace})
	}
	return blocks, nil
}

// indexOfLine finds the byte offset of needle when it occupies its own
// line in s. Returns -1 if not found. Used for the divider / end markers
// so we don't false-match a "=======" inside a SEARCH body.
func indexOfLine(s, needle string) int {
	idx := 0
	for {
		i := strings.Index(s[idx:], needle)
		if i < 0 {
			return -1
		}
		abs := idx + i
		// Check it's at the start of a line.
		if abs > 0 && s[abs-1] != '\n' {
			idx = abs + len(needle)
			continue
		}
		// Check it ends at a newline or EOF.
		end := abs + len(needle)
		if end < len(s) && s[end] != '\n' && s[end] != '\r' {
			idx = end
			continue
		}
		return abs
	}
}

// missDiagnostic builds a useful error for a SEARCH miss. It returns
// the top up-to-3 candidate regions in the file ranked by line-overlap
// with the SEARCH text, with line numbers, so the model can correct the
// SEARCH and retry without re-reading the file.
func missDiagnostic(body, search string, blockNum int) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "block %d: SEARCH not found verbatim. ", blockNum)
	fmt.Fprintln(&sb, "Closest candidates in the current file:")
	candidates := fuzzyCandidates(body, search, 3)
	if len(candidates) == 0 {
		sb.WriteString("  (no similar regions found)\n")
		sb.WriteString("\nCheck whitespace and trailing newlines in your SEARCH block. If the file has changed since you last read it, read it again.")
		return errors.New(sb.String())
	}
	for _, c := range candidates {
		fmt.Fprintf(&sb, "\n— lines %d-%d (overlap %d/%d):\n", c.startLine, c.endLine, c.overlap, c.want)
		for i, ln := range c.lines {
			fmt.Fprintf(&sb, "    %4d │ %s\n", c.startLine+i, ln)
		}
	}
	sb.WriteString("\nFix the SEARCH text to match exactly (whitespace counts), then retry. Do NOT re-read the file — work from the candidates above.")
	return errors.New(sb.String())
}

type candidate struct {
	startLine int
	endLine   int
	lines     []string
	overlap   int
	want      int
}

// fuzzyCandidates finds the top-K windows in body whose lines overlap
// most with the lines of search. Strict equality of trimmed lines is
// the overlap metric — good enough for "you got the indentation wrong"
// or "you missed a comment line" style misses, and cheap.
func fuzzyCandidates(body, search string, k int) []candidate {
	bodyLines := strings.Split(body, "\n")
	searchLines := strings.Split(search, "\n")
	if len(searchLines) == 0 || len(bodyLines) < len(searchLines) {
		return nil
	}
	want := len(searchLines)
	type scored struct {
		start   int
		overlap int
	}
	var hits []scored
	wantTrim := make([]string, len(searchLines))
	for i, l := range searchLines {
		wantTrim[i] = strings.TrimSpace(l)
	}
	for start := 0; start <= len(bodyLines)-want; start++ {
		overlap := 0
		for i := 0; i < want; i++ {
			if strings.TrimSpace(bodyLines[start+i]) == wantTrim[i] {
				overlap++
			}
		}
		if overlap > 0 {
			hits = append(hits, scored{start: start, overlap: overlap})
		}
	}
	// Sort by overlap desc; take top-k. No sort.Slice to avoid the
	// import bloat — small list, do it manually.
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].overlap > hits[j-1].overlap; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	if len(hits) > k {
		hits = hits[:k]
	}
	out := make([]candidate, 0, len(hits))
	for _, h := range hits {
		lines := make([]string, want)
		copy(lines, bodyLines[h.start:h.start+want])
		out = append(out, candidate{
			startLine: h.start + 1,
			endLine:   h.start + want,
			lines:     lines,
			overlap:   h.overlap,
			want:      want,
		})
	}
	return out
}
