package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ChangeKind is what a tool did to a path.
type ChangeKind string

const (
	ChangeCreated ChangeKind = "created"
	ChangeEdited  ChangeKind = "edited"
	ChangeDeleted ChangeKind = "deleted"
	ChangeMoved   ChangeKind = "moved"
	ChangeMkdir   ChangeKind = "mkdir"
)

// Change is one persisted modification record.
type Change struct {
	Path string     `json:"path"`
	Kind ChangeKind `json:"kind"`
	At   time.Time  `json:"at"`
	// From is set on Moved entries — the path the file was renamed away from.
	From string `json:"from,omitempty"`
}

// ChangeTracker records every file mutation a session performs and snapshots
// each file's pre-modification content the first time it's touched. The
// snapshot enables `ageni sessions diff` later — we always have a known
// baseline to compare against, regardless of whether the working tree was
// in git.
//
// Layout under <session_dir>:
//
//	changes.jsonl      append-only log of Change entries
//	snapshots/<sha>    pre-modification content of <abspath> (sha1 of the
//	                   abspath gives a stable, collision-resistant filename)
type ChangeTracker struct {
	mu       sync.Mutex
	metaPath string
	snapDir  string

	// seen tracks paths already snapshotted in this session so subsequent
	// edits don't overwrite the baseline.
	seen map[string]bool

	items []Change
}

// NewChangeTracker opens (or initialises) a tracker rooted at metaPath /
// snapDir. Existing log entries are loaded so resumed sessions keep their
// snapshots and the user sees an unbroken change history.
func NewChangeTracker(metaPath, snapDir string) *ChangeTracker {
	t := &ChangeTracker{
		metaPath: metaPath,
		snapDir:  snapDir,
		seen:     make(map[string]bool),
	}
	_ = os.MkdirAll(snapDir, 0o755) //nolint:gosec
	t.load()
	return t
}

func (t *ChangeTracker) load() {
	f, err := os.Open(t.metaPath) //nolint:gosec
	if err != nil {
		return
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for dec.More() {
		var c Change
		if err := dec.Decode(&c); err != nil {
			break
		}
		t.items = append(t.items, c)
		// Mark seen so we don't re-snapshot the file on a resumed session
		// (which would clobber the original baseline with the post-edit
		// content).
		t.seen[c.Path] = true
		if c.From != "" {
			t.seen[c.From] = true
		}
	}
}

// Snapshot copies the existing content of absPath to the snapshot dir the
// first time we see it. If the file doesn't exist yet (we're about to
// create it), an empty marker is written so diffs against the current file
// render as a pure addition. Idempotent and safe to call without checking.
func (t *ChangeTracker) Snapshot(absPath string) {
	if t == nil || t.snapDir == "" || absPath == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen[absPath] {
		return
	}
	t.seen[absPath] = true
	dst := t.snapPath(absPath)
	if data, err := os.ReadFile(absPath); err == nil { //nolint:gosec
		_ = os.WriteFile(dst, data, 0o644) //nolint:gosec
		return
	}
	// Source didn't exist — write an empty file so diff has something to
	// compare against (creates render as full additions).
	_ = os.WriteFile(dst, nil, 0o644) //nolint:gosec
}

// SnapshotPath returns the snapshot file for absPath. The file may not
// exist if Snapshot was never called for this path.
func (t *ChangeTracker) SnapshotPath(absPath string) string {
	if t == nil {
		return ""
	}
	return t.snapPath(absPath)
}

func (t *ChangeTracker) snapPath(absPath string) string {
	h := sha256.Sum256([]byte(absPath))
	// 16 hex chars (8 bytes) is plenty to avoid collisions for the path
	// names we'll see in one session, and keeps the filename short.
	return filepath.Join(t.snapDir, hex.EncodeToString(h[:8]))
}

// Record appends a change entry and persists it as JSONL. Cheap; safe to
// call frequently. Caller should typically Snapshot the path BEFORE the
// mutation, then Record AFTER it succeeds.
func (t *ChangeTracker) Record(c Change) {
	if t == nil {
		return
	}
	if c.At.IsZero() {
		c.At = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items = append(t.items, c)
	if t.metaPath == "" {
		return
	}
	if dir := filepath.Dir(t.metaPath); dir != "" {
		_ = os.MkdirAll(dir, 0o755) //nolint:gosec
	}
	f, err := os.OpenFile(t.metaPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644) //nolint:gosec
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(&c)
}

// List returns a snapshot of every recorded change. Order is insertion
// order (oldest first).
func (t *ChangeTracker) List() []Change {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Change, len(t.items))
	copy(out, t.items)
	return out
}

// Summary returns one entry per distinct path with the most recent kind +
// timestamp. Used by callers that want "what's been changed this session"
// rather than "every individual edit operation".
func (t *ChangeTracker) Summary() []Change {
	all := t.List()
	by := map[string]Change{}
	order := []string{}
	for _, c := range all {
		if _, seen := by[c.Path]; !seen {
			order = append(order, c.Path)
		}
		// Last write wins for kind/timestamp; preserve creation precedence
		// only if a created+edited+... sequence exists (treat repeated
		// edits as edited; deletion supersedes everything).
		prev, ok := by[c.Path]
		if !ok {
			by[c.Path] = c
			continue
		}
		switch {
		case c.Kind == ChangeDeleted:
			by[c.Path] = c
		case prev.Kind == ChangeCreated && c.Kind == ChangeEdited:
			// Keep created kind but bump timestamp.
			prev.At = c.At
			by[c.Path] = prev
		default:
			by[c.Path] = c
		}
	}
	out := make([]Change, 0, len(order))
	for _, p := range order {
		out = append(out, by[p])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// ResolvePath best-effort matches a user-supplied query to a recorded path.
// Accepts an absolute path, a path relative to cwd, or a unique suffix
// (basename, "pkg/foo.go"). Returns the canonical absolute path used in
// the tracker, or "" if no match (or ambiguous).
func (t *ChangeTracker) ResolvePath(query string) string {
	if query == "" {
		return ""
	}
	if abs, err := filepath.Abs(query); err == nil {
		for _, c := range t.Summary() {
			if c.Path == abs {
				return abs
			}
		}
	}
	matches := []string{}
	for _, c := range t.Summary() {
		if c.Path == query {
			return c.Path
		}
		if strings.HasSuffix(c.Path, "/"+query) || filepath.Base(c.Path) == query {
			matches = append(matches, c.Path)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

