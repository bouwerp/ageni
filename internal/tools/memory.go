package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// memoryMaxBytes is the hard cap for the MEMORY.md file. Keeping it
	// bounded prevents unbounded prompt growth and forces the agent to
	// consolidate stale entries.
	memoryMaxBytes = 3200 // ~800 tokens at 4 bytes/token

	// memoryEntryDelimiter separates memory entries (§ as in hermes-agent).
	memoryEntryDelimiter = "§"
)

// Memory manages a session-persistent MEMORY.md file that is injected into
// the master's system prompt at startup. Entries are § -delimited plain-text
// facts. The file is capped at memoryMaxBytes to keep prompt size bounded.
//
// Design follows hermes-agent's "frozen snapshot + bounded capacity" pattern:
//   - Content is loaded once at startup and injected as a frozen snapshot.
//   - The agent can add/delete entries via the memory_write tool.
//   - A capacity indicator ([N% — X/Y chars]) shows how full the store is.
type Memory struct {
	mu      sync.Mutex
	path    string
	entries []string // current entries (each a single § -terminated chunk)
}

// NewMemory loads (or creates) MEMORY.md at the given path.
func NewMemory(path string) (*Memory, error) {
	m := &Memory{path: path}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Memory) load() error {
	b, err := os.ReadFile(m.path) //nolint:gosec
	if os.IsNotExist(err) {
		return nil // fresh start — no entries yet
	}
	if err != nil {
		return err
	}
	raw := string(b)
	for _, part := range strings.Split(raw, memoryEntryDelimiter) {
		entry := strings.TrimSpace(part)
		if entry != "" {
			m.entries = append(m.entries, entry)
		}
	}
	return nil
}

func (m *Memory) save() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	content := strings.Join(m.entries, "\n"+memoryEntryDelimiter+"\n")
	return os.WriteFile(m.path, []byte(content), 0o600)
}

func (m *Memory) usedBytes() int {
	total := 0
	for _, e := range m.entries {
		total += len(e) + 3 // account for delimiter + newlines
	}
	return total
}

// Snapshot returns the full memory content formatted for system-prompt
// injection. Returns an empty string if no entries exist.
func (m *Memory) Snapshot() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return ""
	}
	used := m.usedBytes()
	pct := used * 100 / memoryMaxBytes
	header := fmt.Sprintf("[%d%% — %d/%d chars]\n", pct, used, memoryMaxBytes)
	return header + strings.Join(m.entries, "\n"+memoryEntryDelimiter+"\n")
}

// MemoryWrite is the tool agents use to add, delete, or consolidate memory
// entries in MEMORY.md.
type MemoryWrite struct {
	Mem *Memory
}

func (MemoryWrite) Name() string { return "memory_write" }
func (MemoryWrite) Description() string {
	return `Manage persistent memory entries that persist across sessions. Entries are injected into every session's system prompt so you remember key facts about the project and user.

Actions:
- add: append a new entry (short, factual; will fail if memory is full — consolidate first)
- delete: remove entry by 1-based index
- consolidate: rewrite all entries from scratch (provide new_entries list)
- list: return current entries with indices and capacity info

Memory is capped at ~800 tokens. When full (100%), you must consolidate before adding.`
}
func (MemoryWrite) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "action":{"type":"string","enum":["add","delete","consolidate","list"],"description":"Operation to perform."},
  "content":{"type":"string","description":"Text of the new entry (for action=add)."},
  "index":{"type":"integer","description":"1-based entry index to delete (for action=delete)."},
  "new_entries":{"type":"array","items":{"type":"string"},"description":"Replacement entries list (for action=consolidate)."}
},
"required":["action"]
}`)
}

func (t MemoryWrite) Call(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Action     string   `json:"action"`
		Content    string   `json:"content"`
		Index      int      `json:"index"`
		NewEntries []string `json:"new_entries"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	m := t.Mem
	m.mu.Lock()
	defer m.mu.Unlock()

	switch p.Action {
	case "list":
		if len(m.entries) == 0 {
			return "(memory is empty)", nil
		}
		used := m.usedBytes()
		pct := used * 100 / memoryMaxBytes
		var sb strings.Builder
		fmt.Fprintf(&sb, "Memory [%d%% — %d/%d chars]:\n", pct, used, memoryMaxBytes)
		for i, e := range m.entries {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, e)
		}
		return sb.String(), nil

	case "add":
		content := strings.TrimSpace(p.Content)
		if content == "" {
			return "", errors.New("content is required for add")
		}
		newUsed := m.usedBytes() + len(content) + 3
		if newUsed > memoryMaxBytes {
			used := m.usedBytes()
			pct := used * 100 / memoryMaxBytes
			return "", fmt.Errorf(
				"memory is at %d%% capacity (%d/%d chars). Consolidate first by calling memory_write(action=consolidate, new_entries=[...]) to merge and trim existing entries before adding new ones.\n\nCurrent entries:\n%s",
				pct, used, memoryMaxBytes, m.listEntries(),
			)
		}
		m.entries = append(m.entries, content)
		if err := m.save(); err != nil {
			return "", fmt.Errorf("memory save: %w", err)
		}
		return fmt.Sprintf("added entry %d (memory now %d%% full)", len(m.entries), m.usedBytes()*100/memoryMaxBytes), nil

	case "delete":
		if p.Index < 1 || p.Index > len(m.entries) {
			return "", fmt.Errorf("index %d out of range (1..%d)", p.Index, len(m.entries))
		}
		removed := m.entries[p.Index-1]
		m.entries = append(m.entries[:p.Index-1], m.entries[p.Index:]...)
		if err := m.save(); err != nil {
			return "", fmt.Errorf("memory save: %w", err)
		}
		return fmt.Sprintf("deleted entry %d: %q", p.Index, removed), nil

	case "consolidate":
		if len(p.NewEntries) == 0 {
			return "", errors.New("new_entries is required for consolidate and must be non-empty")
		}
		// Validate that the consolidated list fits.
		total := 0
		for _, e := range p.NewEntries {
			total += len(strings.TrimSpace(e)) + 3
		}
		if total > memoryMaxBytes {
			return "", fmt.Errorf("consolidated entries (%d chars) still exceed capacity (%d chars). Trim further.", total, memoryMaxBytes)
		}
		m.entries = make([]string, 0, len(p.NewEntries))
		for _, e := range p.NewEntries {
			if t := strings.TrimSpace(e); t != "" {
				m.entries = append(m.entries, t)
			}
		}
		if err := m.save(); err != nil {
			return "", fmt.Errorf("memory save: %w", err)
		}
		return fmt.Sprintf("consolidated to %d entries (memory now %d%% full)", len(m.entries), m.usedBytes()*100/memoryMaxBytes), nil

	default:
		return "", fmt.Errorf("unknown action %q; valid actions: add, delete, consolidate, list", p.Action)
	}
}

func (m *Memory) listEntries() string {
	var sb strings.Builder
	for i, e := range m.entries {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, e)
	}
	return sb.String()
}
