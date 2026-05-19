// Package repomap builds a token-budgeted, ranked summary of a code
// repository's structure for inclusion in the master agent's system prompt.
//
// The map lists each notable file with its top-level symbols (functions,
// types, methods, classes), so the planner has a starting view of the repo
// without paying to read every file. Symbol extraction shells out to
// universal-ctags (`ctags --output-format=json --fields=+n -R`); if ctags
// isn't installed the map silently disables itself.
//
// Ranking is intentionally simple in v1 (recency + todo-mention + small-files-
// first) — Aider's full personalised PageRank over a symbol-reference graph
// would require tree-sitter cgo which complicates cross-compilation. We can
// upgrade later if the simple ranking under-performs.
package repomap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/ageni/internal/homedir"
)

// Symbol is a single definition extracted from a source file.
type Symbol struct {
	Name string
	Kind string // function, method, class, struct, interface, type, var, const
	Line int
}

type SymbolMatch struct {
	Path string
	Lang string
	Name string
	Kind string
	Line int
}

// FileEntry is one file in the map with its extracted symbols.
type FileEntry struct {
	Path     string // relative to repo root
	Lang     string // "go", "ts", "py", ...
	Modified time.Time
	Symbols  []Symbol
}

// RepoMap is the rendered, ranked set of file entries.
type RepoMap struct {
	Root        string // absolute repo root (git toplevel or cwd if not a repo)
	Files       []FileEntry
	Rendered    string // ready for system-prompt injection
	GeneratedAt time.Time
}

// Options tunes Build behaviour.
type Options struct {
	// MaxTokens is the soft cap on rendered output. Approximated as ~4
	// chars per token. Default 2000.
	MaxTokens int

	// FocusFiles are file paths the model is currently working on (e.g.
	// from active todos). They get a ranking boost.
	FocusFiles []string

	// CtagsBin overrides the ctags binary lookup. Empty = lookup PATH.
	CtagsBin string
}

// Build returns a fresh RepoMap for root. Returns nil, nil if ctags is
// missing — callers treat the map as optional.
func Build(ctx context.Context, root string, opts Options) (*RepoMap, error) {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 2000
	}

	bin := opts.CtagsBin
	if bin == "" {
		var err error
		bin, err = exec.LookPath("ctags")
		if err != nil {
			return nil, nil // ctags not installed; map disabled
		}
	}

	files, err := runCtags(ctx, bin, root)
	if err != nil {
		return nil, fmt.Errorf("ctags: %w", err)
	}
	if len(files) == 0 {
		return &RepoMap{Root: root, GeneratedAt: time.Now()}, nil
	}

	scoreAndSort(files, opts.FocusFiles, root)
	rendered, kept := renderToBudget(files, opts.MaxTokens)

	return &RepoMap{
		Root:        root,
		Files:       kept,
		Rendered:    rendered,
		GeneratedAt: time.Now(),
	}, nil
}

// SearchSymbols returns ctags-backed symbol matches ranked by exact/prefix/substring
// match quality and then by file path. Returns nil, nil when ctags is unavailable.
func SearchSymbols(ctx context.Context, root, query string, limit int) ([]SymbolMatch, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	bin, err := exec.LookPath("ctags")
	if err != nil {
		return nil, nil
	}
	files, err := runCtags(ctx, bin, root)
	if err != nil {
		return nil, fmt.Errorf("ctags: %w", err)
	}
	lq := strings.ToLower(query)
	type scored struct {
		match SymbolMatch
		score int
	}
	var scoredMatches []scored
	for _, f := range files {
		for _, s := range f.Symbols {
			ls := strings.ToLower(s.Name)
			score := 0
			switch {
			case ls == lq:
				score = 300
			case strings.HasPrefix(ls, lq):
				score = 200
			case strings.Contains(ls, lq):
				score = 100
			default:
				continue
			}
			if strings.EqualFold(filepath.Base(f.Path), query) {
				score += 25
			}
			scoredMatches = append(scoredMatches, scored{
				match: SymbolMatch{
					Path: f.Path,
					Lang: f.Lang,
					Name: s.Name,
					Kind: s.Kind,
					Line: s.Line,
				},
				score: score,
			})
		}
	}
	sort.Slice(scoredMatches, func(i, j int) bool {
		if scoredMatches[i].score != scoredMatches[j].score {
			return scoredMatches[i].score > scoredMatches[j].score
		}
		if scoredMatches[i].match.Path != scoredMatches[j].match.Path {
			return scoredMatches[i].match.Path < scoredMatches[j].match.Path
		}
		if scoredMatches[i].match.Name != scoredMatches[j].match.Name {
			return scoredMatches[i].match.Name < scoredMatches[j].match.Name
		}
		return scoredMatches[i].match.Line < scoredMatches[j].match.Line
	})
	if len(scoredMatches) > limit {
		scoredMatches = scoredMatches[:limit]
	}
	out := make([]SymbolMatch, 0, len(scoredMatches))
	for _, m := range scoredMatches {
		out = append(out, m.match)
	}
	return out, nil
}

// runCtags invokes ctags and returns one FileEntry per source file with its
// extracted symbols. Streamed JSON parsing so we can handle very large repos
// without holding everything in memory at once.
func runCtags(ctx context.Context, bin, root string) ([]FileEntry, error) {
	cmd := exec.CommandContext(ctx, bin,
		"--output-format=json",
		"--fields=+n",
		"--languages=Go,TypeScript,JavaScript,Python,Rust,Ruby,Java,C,C++,C#,PHP",
		"--exclude=.git",
		"--exclude=node_modules",
		"--exclude=vendor",
		"--exclude=dist",
		"--exclude=build",
		"--exclude=.venv",
		"--exclude=target",
		"-R",
		root,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}

	byPath := map[string]*FileEntry{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var tag struct {
			Type     string `json:"_type"`
			Name     string `json:"name"`
			Path     string `json:"path"`
			Language string `json:"language"`
			Kind     string `json:"kind"`
			Line     int    `json:"line"`
		}
		if err := dec.Decode(&tag); err != nil {
			break
		}
		if tag.Type != "tag" || tag.Name == "" || tag.Path == "" {
			continue
		}
		if !isPublicLikeSymbol(tag.Name, tag.Language) {
			continue
		}
		rel, err := filepath.Rel(root, tag.Path)
		if err != nil {
			rel = tag.Path
		}
		entry, ok := byPath[rel]
		if !ok {
			info, _ := os.Stat(tag.Path)
			mt := time.Time{}
			if info != nil {
				mt = info.ModTime()
			}
			entry = &FileEntry{Path: rel, Lang: tag.Language, Modified: mt}
			byPath[rel] = entry
		}
		entry.Symbols = append(entry.Symbols, Symbol{
			Name: tag.Name,
			Kind: tag.Kind,
			Line: tag.Line,
		})
	}

	files := make([]FileEntry, 0, len(byPath))
	for _, f := range byPath {
		// Sort symbols by line so the rendered map reads top-to-bottom.
		sort.Slice(f.Symbols, func(i, j int) bool { return f.Symbols[i].Line < f.Symbols[j].Line })
		files = append(files, *f)
	}
	return files, nil
}

// isPublicLikeSymbol filters out language-specific private/local symbols
// that aren't useful for an outside reader. Heuristic, not exhaustive.
func isPublicLikeSymbol(name, lang string) bool {
	if name == "" || strings.HasPrefix(name, "_") {
		return false
	}
	switch strings.ToLower(lang) {
	case "go":
		// Exported identifiers start with an uppercase letter.
		return name[0] >= 'A' && name[0] <= 'Z'
	case "python":
		return !strings.HasPrefix(name, "_")
	}
	return true
}

// scoreAndSort assigns each FileEntry a heuristic score and sorts the slice
// in descending order. Files with focus matches go to the top; then recently-
// modified; then alphabetic.
func scoreAndSort(files []FileEntry, focusFiles []string, root string) {
	focusSet := make(map[string]bool, len(focusFiles))
	for _, f := range focusFiles {
		focusSet[normalisePath(f, root)] = true
	}
	now := time.Now()
	type scored struct {
		f     FileEntry
		score float64
	}
	scoredList := make([]scored, 0, len(files))
	for _, f := range files {
		s := 0.0
		if focusSet[f.Path] {
			s += 100
		}
		if !f.Modified.IsZero() {
			ageDays := now.Sub(f.Modified).Hours() / 24
			s += 30 / (1 + ageDays) // recency bump, decays
		}
		// Penalise enormous symbol lists slightly so tiny clear files
		// surface alongside giant grab-bags.
		if len(f.Symbols) > 50 {
			s -= 5
		}
		scoredList = append(scoredList, scored{f, s})
	}
	sort.Slice(scoredList, func(i, j int) bool {
		if scoredList[i].score != scoredList[j].score {
			return scoredList[i].score > scoredList[j].score
		}
		return scoredList[i].f.Path < scoredList[j].f.Path
	})
	for i, s := range scoredList {
		files[i] = s.f
	}
}

func normalisePath(p, root string) string {
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(root, p); err == nil {
			return rel
		}
	}
	return p
}

// renderToBudget formats files as a compact "path: kind sym, kind sym" map
// and stops adding entries once the rendered text would exceed maxTokens
// (estimated at ~4 chars per token). Returns the rendered string and the
// FileEntries that fit.
func renderToBudget(files []FileEntry, maxTokens int) (string, []FileEntry) {
	maxBytes := maxTokens * 4
	var sb strings.Builder
	sb.WriteString("Repository structure (top-ranked files):\n")
	kept := make([]FileEntry, 0, len(files))
	for _, f := range files {
		line := renderFileLine(f)
		if sb.Len()+len(line) > maxBytes && len(kept) > 0 {
			break
		}
		sb.WriteString(line)
		kept = append(kept, f)
	}
	if len(kept) < len(files) {
		fmt.Fprintf(&sb, "\n(map truncated to %d-token budget; %d more files not shown — use grep/glob to find them)\n",
			maxTokens, len(files)-len(kept))
	}
	return sb.String(), kept
}

func renderFileLine(f FileEntry) string {
	if len(f.Symbols) == 0 {
		return f.Path + ":\n"
	}
	var sb strings.Builder
	sb.WriteString(f.Path)
	sb.WriteString(":")
	maxSyms := 12
	for i, s := range f.Symbols {
		if i >= maxSyms {
			fmt.Fprintf(&sb, " …+%d more", len(f.Symbols)-maxSyms)
			break
		}
		sb.WriteString(" ")
		if s.Kind != "" {
			sb.WriteString(s.Kind)
			sb.WriteString(" ")
		}
		sb.WriteString(s.Name)
		if i < len(f.Symbols)-1 && i < maxSyms-1 {
			sb.WriteString(",")
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// CacheKey returns a deterministic key for the current state of root: git
// HEAD + dirty file mtimes (or, if not a git repo, mtimes of every tracked
// file). Used to invalidate ~/.ageni/cache/<key>/repomap.txt.
func CacheKey(root string) (string, error) {
	h := sha256.New()
	h.Write([]byte(root))

	if head, err := gitHead(root); err == nil {
		h.Write([]byte(head))
	}
	if out, err := dirtyFiles(root); err == nil {
		h.Write(out)
	} else {
		// Fall back to walking the tree.
		_ = fs.WalkDir(os.DirFS(root), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := filepath.Base(p)
				if name == ".git" || name == "node_modules" || name == "vendor" {
					return fs.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			fmt.Fprintf(h, "%s:%d:%d\n", p, info.Size(), info.ModTime().UnixNano())
			return nil
		})
	}
	return hex.EncodeToString(h.Sum(nil))[:32], nil
}

func gitHead(root string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func dirtyFiles(root string) ([]byte, error) {
	cmd := exec.Command("git", "status", "--porcelain=v2", "-z")
	cmd.Dir = root
	return cmd.Output()
}

// LoadOrBuild returns a cached map if its key matches; otherwise rebuilds
// and caches. The cache lives at ~/.ageni/cache/repomap/<key>.txt. Returns
// nil, nil silently if ctags is unavailable so callers can treat the map
// as optional.
func LoadOrBuild(ctx context.Context, root string, opts Options) (*RepoMap, error) {
	key, err := CacheKey(root)
	if err != nil {
		return nil, err
	}
	home, err := homedir.Dir()
	if err != nil {
		return Build(ctx, root, opts)
	}
	dir := filepath.Join(home, ".ageni", "cache", "repomap")
	cachePath := filepath.Join(dir, key+".txt")
	if data, err := os.ReadFile(cachePath); err == nil { //nolint:gosec
		return &RepoMap{Root: root, Rendered: string(data), GeneratedAt: time.Now()}, nil
	}
	m, err := Build(ctx, root, opts)
	if err != nil || m == nil {
		return m, err
	}
	if m.Rendered != "" {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(cachePath, []byte(m.Rendered), 0o644) //nolint:gosec
	}
	return m, nil
}
