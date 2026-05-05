// Package skills loads on-demand instruction bundles ("skills") from
// ~/.ageni/skills/ and ./.ageni/skills/. Each skill is a directory containing
// a SKILL.md file with YAML frontmatter (name, description, version) followed
// by the body in markdown. The master sees only a one-line catalog
// (name: description) in its system prompt; the body is loaded on demand via
// the read_skill tool when the agent decides a skill is relevant.
package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is one loaded SKILL.md.
type Skill struct {
	Name        string
	Description string
	Version     string
	Body        string
	Path        string // source SKILL.md
	Topics      map[string]string
}

// Registry holds all discovered skills, keyed by name. Project skills
// override global skills with the same name.
type Registry struct {
	skills map[string]*Skill
	order  []string
}

// Load walks the standard search paths and parses every SKILL.md found.
// Returns an empty registry if no skills are present (skills are optional).
func Load() (*Registry, error) {
	r := &Registry{skills: map[string]*Skill{}}
	for _, root := range searchPaths() {
		if err := r.loadFrom(root); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	r.rebuildOrder()
	return r, nil
}

func searchPaths() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".ageni", "skills"))
	}
	paths = append(paths, filepath.Join(".ageni", "skills"))
	return paths
}

func (r *Registry) loadFrom(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		s, err := parseSkillDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ageni: skill %s skipped: %v\n", dir, err)
			continue
		}
		// Project paths come second; overwrite global on name collision.
		r.skills[s.Name] = s
	}
	return nil
}

func parseSkillDir(dir string) (*Skill, error) {
	skillPath := filepath.Join(dir, "SKILL.md")
	b, err := os.ReadFile(skillPath) //nolint:gosec
	if err != nil {
		return nil, err
	}
	name, desc, version, body, err := parseFrontmatter(string(b))
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = filepath.Base(dir)
	}

	s := &Skill{
		Name:        name,
		Description: desc,
		Version:     version,
		Body:        body,
		Path:        skillPath,
		Topics:      map[string]string{},
	}

	// Topics live in topics/<name>.md — cheap to enumerate without reading.
	topicsDir := filepath.Join(dir, "topics")
	if entries, err := os.ReadDir(topicsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				tname := strings.TrimSuffix(e.Name(), ".md")
				if tb, err := os.ReadFile(filepath.Join(topicsDir, e.Name())); err == nil { //nolint:gosec
					s.Topics[tname] = string(tb)
				}
			}
		}
	}
	return s, nil
}

// parseFrontmatter parses a YAML frontmatter block delimited by "---" lines
// at the top of the file. Returns name, description, version, and the body
// after the frontmatter. The frontmatter parser is intentionally minimal —
// we only support flat key:value pairs (the SKILL.md format never uses
// nested structures).
func parseFrontmatter(content string) (name, desc, version, body string, err error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", "", content, nil
	}
	// Find closing fence.
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", "", "", errors.New("frontmatter is not closed")
	}

	// Parse key:value pairs in lines[1:end]. Support multi-line values that
	// continue on indented lines (description often wraps).
	var key, val string
	flush := func() {
		switch key {
		case "name":
			name = strings.TrimSpace(val)
		case "description":
			desc = strings.TrimSpace(val)
		case "version":
			version = strings.TrimSpace(val)
		}
		key, val = "", ""
	}
	for i := 1; i < end; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			// New key.
			if key != "" {
				flush()
			}
			i := strings.Index(trimmed, ":")
			if i <= 0 {
				continue
			}
			key = strings.TrimSpace(trimmed[:i])
			val = strings.TrimSpace(trimmed[i+1:])
		} else {
			// Continuation.
			val += " " + trimmed
		}
	}
	if key != "" {
		flush()
	}

	body = strings.Join(lines[end+1:], "\n")
	return name, desc, version, strings.TrimLeft(body, "\n"), nil
}

func (r *Registry) rebuildOrder() {
	r.order = r.order[:0]
	for k := range r.skills {
		r.order = append(r.order, k)
	}
	sort.Strings(r.order)
}

// Names returns skill names in alphabetical order (deterministic for prompt
// caching).
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Get returns a skill by name, or nil if not found.
func (r *Registry) Get(name string) *Skill { return r.skills[name] }

// All returns all skills in alphabetical order.
func (r *Registry) All() []*Skill {
	out := make([]*Skill, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.skills[n])
	}
	return out
}

// Catalog returns a compact "<name>: <description>" listing for inclusion in
// the master/sub-agent system prompt. Empty string when no skills are loaded.
func (r *Registry) Catalog() string {
	if len(r.order) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, n := range r.order {
		s := r.skills[n]
		sb.WriteString("- " + s.Name + ": " + s.Description + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
