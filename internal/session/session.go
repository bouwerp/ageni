package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session is the per-instance state container. Every persistent artifact —
// session log, todo list, corrections log, future indexes — lives under
// Dir keyed by the session ID, so multiple ageni instances in the same
// repo (or different repos) never collide.
type Session struct {
	ID       string    // unique identifier; timestamp + short rand
	Dir      string    // absolute path to the session directory
	RepoPath string    // absolute path to the project root the session was created in
	Started  time.Time // when the session was first created
	LastUsed time.Time // updated on each Touch()

	// Model snapshot at session start. Useful for `ageni sessions list`
	// and debugging — what config was active. Doesn't change on reload.
	MasterProvider   string `json:",omitempty"`
	MasterModel      string `json:",omitempty"`
	SubagentProvider string `json:",omitempty"`
	SubagentModel    string `json:",omitempty"`
}

// SessionsRoot returns the directory all sessions live under
// (~/.ageni/sessions). Created on first call.
func SessionsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".ageni", "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

// New starts a fresh session and writes its metadata to disk. repoPath is
// the project root (or empty if the session isn't associated with one).
// Each session gets a unique ID like "20260505-143052-7a8f" — sortable by
// time and short enough to type when resuming.
func New(repoPath string) (*Session, error) {
	root, err := SessionsRoot()
	if err != nil {
		return nil, err
	}
	id, err := genID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Session{
		ID:       id,
		Dir:      dir,
		RepoPath: repoPath,
		Started:  now,
		LastUsed: now,
	}
	if err := s.saveMeta(); err != nil {
		return nil, err
	}
	return s, nil
}

// Open loads an existing session by ID and stamps LastUsed.
// Use this when actively resuming a session; for read-only access (e.g.
// listing) use readMeta which never touches the timestamp.
func Open(id string) (*Session, error) {
	root, err := SessionsRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, id)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("no such session %q at %s", id, dir)
	}
	s, err := readMeta(dir, id)
	if err != nil {
		// Fallback if meta unreadable.
		s = &Session{ID: id, Dir: dir, Started: info.ModTime()}
	}
	s.LastUsed = time.Now()
	_ = s.saveMeta()
	return s, nil
}

// List returns all sessions sorted by LastUsed (most recent first).
// Uses a read-only loader so listing never mutates LastUsed on disk.
func List() ([]*Session, error) {
	root, err := SessionsRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := readMeta(filepath.Join(root, e.Name()), e.Name())
		if err != nil {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	return out, nil
}

// readMeta loads session metadata without touching LastUsed on disk.
// Used by List() and the picker so browsing sessions doesn't corrupt sort order.
func readMeta(dir, id string) (*Session, error) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.json")) //nolint:gosec
	if err != nil {
		// Missing meta.json (pre-v0.37.20 session). Use log.jsonl mtime as a
		// better proxy for LastUsed than the directory mtime, which only
		// reflects file creation, not subsequent writes.
		lastUsed := bestLastUsed(dir)
		return &Session{ID: id, Dir: dir, Started: lastUsed, LastUsed: lastUsed}, nil
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	s.Dir = dir
	return &s, nil
}

// bestLastUsed returns the best available proxy for when a session was last
// active. Checks log.jsonl, then todo.json, then the directory itself.
func bestLastUsed(dir string) time.Time {
	for _, name := range []string{"log.jsonl", "todo.json", "corrections.jsonl"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return info.ModTime()
		}
	}
	if info, err := os.Stat(dir); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// Touch updates LastUsed and persists. Cheap; safe to call frequently.
func (s *Session) Touch() {
	s.LastUsed = time.Now()
	_ = s.saveMeta()
}

// Path returns an absolute path inside the session directory for the
// given relative file. Used by todo / log / corrections / future stores.
func (s *Session) Path(name string) string {
	return filepath.Join(s.Dir, name)
}

// SetModels records the model configuration so `ageni sessions list` can
// show it.
func (s *Session) SetModels(masterProvider, masterModel, subProvider, subModel string) {
	s.MasterProvider = masterProvider
	s.MasterModel = masterModel
	s.SubagentProvider = subProvider
	s.SubagentModel = subModel
	_ = s.saveMeta()
}

func (s *Session) saveMeta() error {
	if s.Dir == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, "meta.json"), b, 0o600) //nolint:gosec
}

// genID returns "<YYYYMMDD-HHMMSS>-<4 hex>". Sortable by time, unique
// enough across processes started within the same second.
func genID() (string, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	now := time.Now().Format("20060102-150405")
	return now + "-" + hex.EncodeToString(b[:]), nil
}

// ResolveID accepts either a full session ID or a unique prefix (handy
// when typing on the CLI). Returns the resolved full ID or an error if
// the prefix matches zero or more than one session.
func ResolveID(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "", fmt.Errorf("session id is required")
	}
	all, err := List()
	if err != nil {
		return "", err
	}
	matches := make([]string, 0, len(all))
	for _, s := range all {
		if s.ID == prefix {
			return s.ID, nil // exact match wins
		}
		if strings.HasPrefix(s.ID, prefix) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matches %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous prefix %q: matches %d sessions (%s)", prefix, len(matches), strings.Join(matches, ", "))
	}
}
