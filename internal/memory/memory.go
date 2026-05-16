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
	if home, err := os.UserHomeDir(); err == nil {
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

	if len(mems) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<memories>\n")
	sb.WriteString("Persistent facts and context snippets stored across sessions. Trust these over your general knowledge when they conflict.\n\n")
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

// Names returns all memory keys in alphabetical order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}
