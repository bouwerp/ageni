package roles

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed embedded
var embeddedFS embed.FS

// RoleDefinition describes a predefined agent persona. Roles bundle a
// system-prompt addendum, tool access, model tier, budget, and optional
// skill pin so the master can spawn a consistent persona via a single
// "role" parameter rather than configuring each field individually.
type RoleDefinition struct {
	// Identity
	Name        string `yaml:"name"`
	Description string `yaml:"description"` // shown to master for routing decisions

	// Compute & budget defaults (overridable at spawn time)
	ModelTier       string `yaml:"model_tier"`        // haiku | sonnet | opus
	BudgetToolCalls int    `yaml:"budget_tool_calls"` // 0 = use system default

	// Capability & skill
	UseSkill            string   `yaml:"use_skill"`            // optional skill to auto-pin
	RequiredCapabilities []string `yaml:"required_capabilities"` // e.g. ["vision"]

	// Persona
	SystemPromptAddition string `yaml:"system_prompt_addition"`
	TaskBoundaries       string `yaml:"task_boundaries"`

	// Authority
	CanDelegate bool `yaml:"can_delegate"`
}

// Registry holds the set of loaded roles, keyed by name.
type Registry struct {
	roles map[string]RoleDefinition
}

// Global is the process-wide role registry populated at startup.
var Global = &Registry{roles: make(map[string]RoleDefinition)}

// Load populates r by merging embedded built-in roles with any roles found in
// userDir (typically ~/.ageni/roles/). User-defined roles override built-ins
// with the same name.
func Load(userDir string) (*Registry, error) {
	r := &Registry{roles: make(map[string]RoleDefinition)}

	// 1. Load embedded built-in roles.
	if err := r.loadFS(embeddedFS, "embedded"); err != nil {
		return nil, fmt.Errorf("loading embedded roles: %w", err)
	}

	// 2. Load user-defined roles (override built-ins).
	if userDir != "" {
		if _, err := os.Stat(userDir); err == nil {
			if err := r.loadDir(userDir); err != nil {
				return nil, fmt.Errorf("loading user roles from %s: %w", userDir, err)
			}
		}
	}

	return r, nil
}

// loadFS reads role definitions from an embed.FS rooted at root.
func (r *Registry) loadFS(fsys embed.FS, root string) error {
	return fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isRoleFile(d.Name()) {
			return nil
		}
		data, err := fsys.ReadFile(path)
		if err != nil {
			return err
		}
		return r.parse(data)
	})
}

// loadDir reads role definitions from a directory on disk.
func (r *Registry) loadDir(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isRoleFile(d.Name()) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return r.parse(data)
	})
}

func (r *Registry) parse(data []byte) error {
	var def RoleDefinition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return err
	}
	if def.Name == "" {
		return nil // skip malformed entries
	}
	r.roles[strings.ToLower(def.Name)] = def
	return nil
}

// Lookup returns the RoleDefinition for the given name (case-insensitive).
func (r *Registry) Lookup(name string) (RoleDefinition, bool) {
	def, ok := r.roles[strings.ToLower(name)]
	return def, ok
}

// Names returns a sorted list of all registered role names.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.roles))
	for n := range r.roles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Catalog returns a human-readable summary block suitable for injection into
// system prompts (one line per role: "name – description").
func (r *Registry) Catalog() string {
	names := r.Names()
	var sb strings.Builder
	for _, n := range names {
		def := r.roles[n]
		sb.WriteString(def.Name)
		sb.WriteString(" – ")
		sb.WriteString(def.Description)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func isRoleFile(name string) bool {
	return strings.EqualFold(name, "ROLE.yaml") || strings.EqualFold(name, "ROLE.yml")
}
