// Package memory provides a lightweight persistent key-value store for facts
// and context snippets that the agent (or the user) wants to remember across
// sessions.
//
// A memory is a small markdown file stored under:
//
//	~/.ageni/memories/<key>.md        (global)
//	./.ageni/memories/<key>.md        (project-local, overrides global)
//
// Each file may have YAML frontmatter with a "description" field (one-liner
// shown in the catalog), followed by the memory content. Files without
// frontmatter use their entire content as the body and their key as the
// description.
//
// The Registry is live: Set/Delete modify both the in-memory map and the
// backing files immediately.
package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/bouwerp/ageni/internal/homedir"
)

// Memory is one stored fact/snippet.
type Memory struct {
	Key         string
	Description string
	Content     string
	Path        string // backing file path
}

// Registry is a live, thread-safe collection of memories.
type Registry struct {
	mu      sync.RWMutex
	items   map[string]*Memory
	order   []string
	writeAt string // directory for new writes (project-local preferred)
}

// Load reads memories from the search paths in priority order (lowest to
// highest):
//
//  1. ~/.ageni/memories/   (global)
//  2. ./.ageni/memories/   (project-local, overrides global)
//
// Later sources win on key collision so project-local memories shadow global
// ones.
func Load() (*Registry, error) {
	r := &Registry{items: map[string]*Memory{}}

	global := ""
	if home, err := homedir.Dir(); err == nil {
		global = filepath.Join(home, ".ageni", "memories")
	}
	local := filepath.Join(".ageni", "memories")

	// Load global first, then project-local (project wins on collision).
	if global != "" {
		_ = r.loadFrom(global)
	}
	_ = r.loadFrom(local)

	// Write target: project-local when .ageni/ exists, otherwise global.
	if _, err := os.Stat(".ageni"); err == nil {
		r.writeAt = local
	} else if global != "" {
		r.writeAt = global
	}

	r.rebuildOrder()
	return r, nil
}

func (r *Registry) loadFrom(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".md")
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path) //nolint:gosec
		if err != nil {
			continue
		}
		desc, content := parseFrontmatter(string(b))
		if desc == "" {
			desc = key
		}
		r.items[key] = &Memory{
			Key:         key,
			Description: desc,
			Content:     strings.TrimSpace(content),
			Path:        path,
		}
	}
	return nil
}

// parseFrontmatter extracts a "description" value from optional YAML
// frontmatter at the top of the file (delimited by "---" lines). Returns
// the description and the body after the frontmatter.
func parseFrontmatter(content string) (description, body string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", content
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", content
	}
	for _, line := range lines[1:end] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
		}
	}
	body = strings.Join(lines[end+1:], "\n")
	return description, body
}

func (r *Registry) rebuildOrder() {
	r.order = r.order[:0]
	for k := range r.items {
		r.order = append(r.order, k)
	}
	sort.Strings(r.order)
}

// All returns all memories in alphabetical key order.
func (r *Registry) All() []*Memory {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Memory, 0, len(r.order))
	for _, k := range r.order {
		out = append(out, r.items[k])
	}
	return out
}

// Get returns a memory by key, or nil if not found.
func (r *Registry) Get(key string) *Memory {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.items[key]
}

// Set creates or updates a memory, persisting it to disk immediately.
func (r *Registry) Set(key, description, content string) error {
	if key == "" {
		return errors.New("memory key cannot be empty")
	}
	if r.writeAt == "" {
		return errors.New("no writable memory directory found")
	}
	if err := os.MkdirAll(r.writeAt, 0750); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	path := filepath.Join(r.writeAt, key+".md")
	var sb strings.Builder
	sb.WriteString("---\ndescription: ")
	sb.WriteString(description)
	sb.WriteString("\n---\n")
	sb.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		sb.WriteByte('\n')
	}

	if err := os.WriteFile(path, []byte(sb.String()), 0640); err != nil { //nolint:gosec
		return fmt.Errorf("write memory: %w", err)
	}

	r.mu.Lock()
	r.items[key] = &Memory{
		Key:         key,
		Description: description,
		Content:     strings.TrimSpace(content),
		Path:        path,
	}
	r.rebuildOrder()
	r.mu.Unlock()
	return nil
}

// Delete removes a memory by key from both the in-memory map and disk.
func (r *Registry) Delete(key string) error {
	r.mu.Lock()
	m := r.items[key]
	if m == nil {
		r.mu.Unlock()
		return fmt.Errorf("no memory with key %q", key)
	}
	path := m.Path
	delete(r.items, key)
	r.rebuildOrder()
	r.mu.Unlock()

	if path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete memory file: %w", err)
		}
	}
	return nil
}

// InlineBlock returns a <memories>...</memories> string suitable for injection
// into a system prompt. Returns "" when the registry is empty.
func (r *Registry) InlineBlock() string {
	r.mu.RLock()
	mems := make([]*Memory, 0, len(r.order))
	for _, k := range r.order {
		mems = append(mems, r.items[k])
	}
	r.mu.RUnlock()

	return renderInlineBlock(mems, "")
}

// InlineBlockForQuery returns a <memories>...</memories> block containing only
// the most relevant memories for the given query when relevance can be
// determined. When the query is empty or the simple ranker cannot find any
// matches, it falls back to InlineBlock() so behavior stays conservative.
func (r *Registry) InlineBlockForQuery(query string, maxItems int) string {
	r.mu.RLock()
	mems := make([]*Memory, 0, len(r.order))
	for _, k := range r.order {
		mems = append(mems, r.items[k])
	}
	r.mu.RUnlock()

	if len(mems) == 0 || maxItems <= 0 || len(mems) <= maxItems {
		return renderInlineBlock(mems, "")
	}
	terms := queryTerms(query)
	if len(terms) == 0 {
		return renderInlineBlock(mems, "")
	}

	type scoredMemory struct {
		mem   *Memory
		score int
	}
	scored := make([]scoredMemory, 0, len(mems))
	for _, m := range mems {
		score := scoreMemory(m, terms)
		if score > 0 {
			scored = append(scored, scoredMemory{mem: m, score: score})
		}
	}
	if len(scored) == 0 {
		return renderInlineBlock(mems, "")
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].mem.Key < scored[j].mem.Key
	})
	if len(scored) > maxItems {
		scored = scored[:maxItems]
	}
	selected := make([]*Memory, 0, len(scored))
	for _, item := range scored {
		selected = append(selected, item.mem)
	}
	note := fmt.Sprintf("Showing %d of %d memories most relevant to the current request.\n\n", len(selected), len(mems))
	return renderInlineBlock(selected, note)
}

func renderInlineBlock(mems []*Memory, note string) string {
	if len(mems) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<memories>\n")
	sb.WriteString("Persistent facts and context snippets stored across sessions. Trust these over your general knowledge when they conflict.\n\n")
	if note != "" {
		sb.WriteString(note)
	}
	for _, m := range mems {
		sb.WriteString("**")
		sb.WriteString(m.Key)
		sb.WriteString("** — ")
		sb.WriteString(m.Description)
		sb.WriteString("\n")
		for _, line := range strings.Split(m.Content, "\n") {
			sb.WriteString("  ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Use remember(key=...) to add/update memories, forget(key=...) to remove them.\n")
	sb.WriteString("</memories>")
	return sb.String()
}

func queryTerms(query string) []string {
	seen := map[string]struct{}{}
	terms := make([]string, 0, 8)
	for _, raw := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(raw) < 3 {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		terms = append(terms, raw)
	}
	return terms
}

func scoreMemory(m *Memory, terms []string) int {
	key := strings.ToLower(m.Key)
	desc := strings.ToLower(m.Description)
	body := strings.ToLower(m.Content)
	score := 0
	for _, term := range terms {
		if strings.Contains(key, term) {
			score += 4
		}
		if strings.Contains(desc, term) {
			score += 3
		}
		if strings.Contains(body, term) {
			score++
		}
	}
	return score
}

// Names returns all memory keys in alphabetical order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}
