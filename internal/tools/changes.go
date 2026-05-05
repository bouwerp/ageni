package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	// Step is the per-session monotonic counter of mutations. Zero on
	// entries written before step tracking landed (v0.22.0–v0.28.0).
	Step int `json:"step,omitempty"`
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
	mu             sync.Mutex
	metaPath       string
	snapDir        string
	checkpointsDir string

	// seen tracks paths already snapshotted in this session so subsequent
	// edits don't overwrite the baseline.
	seen map[string]bool

	items    []Change
	nextStep int
}

// NewChangeTracker opens (or initialises) a tracker rooted at metaPath /
// snapDir. Existing log entries are loaded so resumed sessions keep their
// snapshots and the user sees an unbroken change history.
func NewChangeTracker(metaPath, snapDir string) *ChangeTracker {
	// checkpointsDir is a sibling of snapDir under the session dir,
	// holding per-step snapshots (separate from the v0.22 first-touch
	// baseline snapshots in snapDir).
	checkpoints := filepath.Join(filepath.Dir(snapDir), "checkpoints")
	t := &ChangeTracker{
		metaPath:       metaPath,
		snapDir:        snapDir,
		checkpointsDir: checkpoints,
		seen:           make(map[string]bool),
	}
	_ = os.MkdirAll(snapDir, 0o755)     //nolint:gosec
	_ = os.MkdirAll(checkpoints, 0o755) //nolint:gosec
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
		if c.Step > t.nextStep {
			t.nextStep = c.Step
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

// BeginMutation captures the current state of absPath into a fresh
// per-step checkpoint and returns the step number. Also captures the
// first-touch baseline if absPath hasn't been seen yet (so diffs against
// the original session state still work). Tools should call this before
// mutating, then pass the returned step into Change.Step on Record.
//
// If absPath doesn't exist (we're about to create it), the per-step
// checkpoint is omitted — Rewind uses the change kind to decide whether
// to delete a path or restore content.
func (t *ChangeTracker) BeginMutation(absPath string) int {
	if t == nil || absPath == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextStep++
	step := t.nextStep

	// Baseline (idempotent — first-touch only).
	if !t.seen[absPath] {
		t.seen[absPath] = true
		dst := t.snapPathLocked(absPath)
		if data, err := os.ReadFile(absPath); err == nil { //nolint:gosec
			_ = os.WriteFile(dst, data, 0o644) //nolint:gosec
		} else {
			_ = os.WriteFile(dst, nil, 0o644) //nolint:gosec
		}
	}

	// Per-step checkpoint of current content. Skip if file doesn't exist
	// — Rewind will infer "didn't exist before" from the change kind.
	stepDir := filepath.Join(t.checkpointsDir, fmt.Sprintf("step-%05d", step))
	if data, err := os.ReadFile(absPath); err == nil { //nolint:gosec
		_ = os.MkdirAll(stepDir, 0o755) //nolint:gosec
		dst := filepath.Join(stepDir, hashPath(absPath))
		_ = os.WriteFile(dst, data, 0o644) //nolint:gosec
	}
	return step
}

// snapPathLocked is snapPath without the lock — caller must hold
// t.mu. Used by BeginMutation since it already locks once.
func (t *ChangeTracker) snapPathLocked(absPath string) string {
	return t.snapPath(absPath)
}

func hashPath(absPath string) string {
	h := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(h[:8])
}

// Rewind restores the workspace to the state BEFORE the given step. For
// each path that was first mutated at step >= toStep, the path's
// pre-mutation content is restored from the corresponding per-step
// checkpoint. Paths that were CREATED at step >= toStep are deleted.
//
// Returns the list of paths actually touched, in stable order. Callers
// should also truncate the conversation tail (and reset the master's
// message buffer) — Rewind only handles the workspace.
func (t *ChangeTracker) Rewind(toStep int) ([]string, error) {
	if t == nil {
		return nil, nil
	}
	t.mu.Lock()
	items := make([]Change, len(t.items))
	copy(items, t.items)
	t.mu.Unlock()

	// For each path, find its earliest change at step >= toStep. That
	// step's per-step checkpoint holds the BEFORE-state.
	earliest := map[string]Change{}
	for _, c := range items {
		if c.Step < toStep {
			continue
		}
		if existing, ok := earliest[c.Path]; !ok || c.Step < existing.Step {
			earliest[c.Path] = c
		}
		// Move source path also gets its pre-move state restored.
		if c.From != "" {
			if existing, ok := earliest[c.From]; !ok || c.Step < existing.Step {
				earliest[c.From] = c
			}
		}
	}

	var touched []string
	for path, c := range earliest {
		switch c.Kind {
		case ChangeCreated, ChangeMkdir:
			// Path didn't exist before this step — remove.
			_ = os.RemoveAll(path)
		default:
			// Restore from the per-step checkpoint.
			snap := filepath.Join(t.checkpointsDir, fmt.Sprintf("step-%05d", c.Step), hashPath(path))
			data, err := os.ReadFile(snap) //nolint:gosec
			if err != nil {
				continue // no checkpoint for this path at this step
			}
			if dir := filepath.Dir(path); dir != "" {
				_ = os.MkdirAll(dir, 0o755) //nolint:gosec
			}
			if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec
				continue
			}
		}
		touched = append(touched, path)
	}
	sort.Strings(touched)
	return touched, nil
}

// Checkpoints returns each step number with its associated changes,
// oldest first. Used by `ageni sessions checkpoints` to show the user
// what they can rewind to.
func (t *ChangeTracker) Checkpoints() []CheckpointInfo {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	items := make([]Change, len(t.items))
	copy(items, t.items)
	t.mu.Unlock()

	bySteps := map[int]*CheckpointInfo{}
	var order []int
	for _, c := range items {
		if c.Step == 0 {
			continue // pre-step entries (v0.22)
		}
		ci, ok := bySteps[c.Step]
		if !ok {
			ci = &CheckpointInfo{Step: c.Step, At: c.At}
			bySteps[c.Step] = ci
			order = append(order, c.Step)
		}
		ci.Changes = append(ci.Changes, c)
	}
	sort.Ints(order)
	out := make([]CheckpointInfo, 0, len(order))
	for _, s := range order {
		out = append(out, *bySteps[s])
	}
	return out
}

// CheckpointInfo summarises one per-session checkpoint step.
type CheckpointInfo struct {
	Step    int
	At      time.Time
	Changes []Change
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

