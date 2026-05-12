package secrets

import (
	"sort"
	"strings"
	"sync"
)

// Redactor maintains a registry of sensitive values and scrubs them from
// arbitrary strings before those strings can enter the LLM context window.
//
// Usage:
//
//	r := NewRedactor()
//	r.Register("ANTHROPIC_API_KEY", "sk-ant-api03-...")
//	safe := r.Scrub(someToolOutput) // "sk-ant-api03-..." → "[REDACTED:ANTHROPIC_API_KEY]"
type Redactor struct {
	mu      sync.RWMutex
	entries []redactEntry // sorted longest-value-first to avoid partial matches
}

type redactEntry struct {
	alias string
	value string
}

// NewRedactor returns an empty, thread-safe Redactor.
func NewRedactor() *Redactor {
	return &Redactor{}
}

// Register adds alias → value to the redaction set. Subsequent calls to
// Scrub will replace any occurrence of value with [REDACTED:<alias>].
// Values shorter than 4 characters are ignored (too short to be meaningful
// secrets and would cause false positives on common substrings).
func (r *Redactor) Register(alias, value string) {
	if len(value) < 4 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Update existing entry if alias already registered.
	for i, e := range r.entries {
		if e.alias == alias {
			r.entries[i].value = value
			r.sortLocked()
			return
		}
	}
	r.entries = append(r.entries, redactEntry{alias: alias, value: value})
	r.sortLocked()
}

// Unregister removes an alias from the redaction set (e.g. after deletion).
func (r *Redactor) Unregister(alias string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.entries[:0]
	for _, e := range r.entries {
		if e.alias != alias {
			out = append(out, e)
		}
	}
	r.entries = out
}

// Scrub returns s with all registered secret values replaced by their
// [REDACTED:<alias>] placeholder. The replacement is case-sensitive and
// processes longer values first to avoid partial-replacement of longer keys
// that share a common prefix.
func (r *Redactor) Scrub(s string) string {
	if s == "" {
		return s
	}
	r.mu.RLock()
	entries := make([]redactEntry, len(r.entries))
	copy(entries, r.entries)
	r.mu.RUnlock()

	for _, e := range entries {
		if e.value != "" && strings.Contains(s, e.value) {
			s = strings.ReplaceAll(s, e.value, "[REDACTED:"+e.alias+"]")
		}
	}
	return s
}

// HasSecrets returns true if any registered secret value appears in s.
// Useful for fast pre-checks before doing more expensive operations.
func (r *Redactor) HasSecrets(s string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if e.value != "" && strings.Contains(s, e.value) {
			return true
		}
	}
	return false
}

// Count returns the number of registered aliases.
func (r *Redactor) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// sortLocked sorts entries by value length descending. Must be called with
// write lock held.
func (r *Redactor) sortLocked() {
	sort.Slice(r.entries, func(i, j int) bool {
		return len(r.entries[i].value) > len(r.entries[j].value)
	})
}
