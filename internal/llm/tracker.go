package llm

import (
	"sort"
	"sync"
)

// Tracker accumulates token usage across calls, attributed by role label
// (e.g. "master", "subagent:abc123") and model.
type Tracker struct {
	mu      sync.Mutex
	entries map[trackerKey]Usage
	subs    []chan TrackerSnapshot
}

type trackerKey struct {
	Role  string
	Model string
}

type TrackerEntry struct {
	Role  string
	Model string
	Usage Usage
}

type TrackerSnapshot struct {
	Entries []TrackerEntry
	Total   Usage
}

func NewTracker() *Tracker {
	return &Tracker{entries: make(map[trackerKey]Usage)}
}

// Add records usage for a (role, model) pair. Safe for concurrent use.
func (t *Tracker) Add(role, model string, u Usage) {
	t.mu.Lock()
	k := trackerKey{Role: role, Model: model}
	cur := t.entries[k]
	cur.InputTokens += u.InputTokens
	cur.OutputTokens += u.OutputTokens
	cur.CacheReadTokens += u.CacheReadTokens
	cur.CacheCreationTokens += u.CacheCreationTokens
	t.entries[k] = cur
	snap := t.snapshotLocked()
	subs := append([]chan TrackerSnapshot(nil), t.subs...)
	t.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// Snapshot returns a copy of current totals.
func (t *Tracker) Snapshot() TrackerSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

func (t *Tracker) snapshotLocked() TrackerSnapshot {
	snap := TrackerSnapshot{Entries: make([]TrackerEntry, 0, len(t.entries))}
	for k, u := range t.entries {
		snap.Entries = append(snap.Entries, TrackerEntry{Role: k.Role, Model: k.Model, Usage: u})
		snap.Total.InputTokens += u.InputTokens
		snap.Total.OutputTokens += u.OutputTokens
		snap.Total.CacheReadTokens += u.CacheReadTokens
		snap.Total.CacheCreationTokens += u.CacheCreationTokens
	}
	sort.Slice(snap.Entries, func(i, j int) bool {
		if snap.Entries[i].Role != snap.Entries[j].Role {
			return snap.Entries[i].Role < snap.Entries[j].Role
		}
		return snap.Entries[i].Model < snap.Entries[j].Model
	})
	return snap
}

// Subscribe returns a buffered channel that receives a snapshot on every Add.
// The channel is non-blocking on the sender side: if the subscriber is slow,
// updates are dropped.
func (t *Tracker) Subscribe() <-chan TrackerSnapshot {
	ch := make(chan TrackerSnapshot, 4)
	t.mu.Lock()
	t.subs = append(t.subs, ch)
	t.mu.Unlock()
	return ch
}
