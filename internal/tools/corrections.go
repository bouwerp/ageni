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
	"time"
)

// Correction is one append-only entry in corrections.jsonl. Recorded
// whenever the master overrides or refines a prior conclusion — keeps
// future workers and turns from re-believing stale facts.
type Correction struct {
	At  time.Time `json:"at"`
	Was string    `json:"was"`
	Now string    `json:"now"`
	Why string    `json:"why,omitempty"`
}

// RecordCorrection is a master-only tool that appends one entry to the
// session's corrections.jsonl. Most-recent entries are surfaced back to
// the master via the active-context tail block on every turn, and the
// master can copy the relevant subset into a worker's prior_findings
// when it spawns.
type RecordCorrection struct {
	mu   sync.Mutex
	Path string // absolute path to corrections.jsonl
}

// NewRecordCorrection takes the absolute path to the session's corrections
// log. Pass an empty string to disable persistence (in-memory only).
func NewRecordCorrection(path string) *RecordCorrection {
	return &RecordCorrection{Path: path}
}

func (*RecordCorrection) Name() string { return "record_correction" }

func (*RecordCorrection) Description() string {
	return `Record a correction when you've contradicted or refined a prior conclusion this session. The entry is appended to the session's corrections.jsonl and the most-recent entries are surfaced back to you on every turn so stale facts don't keep getting referenced.

Use when:
- A worker reported X, you've since established X is wrong (or only partially right).
- You changed your plan mid-task — record what changed and why.
- A user correction lands and you need future you (and any newly-spawned workers) to honour it.

Don't use for routine "I'm doing X next" updates — that's what todo_write is for.`
}

func (*RecordCorrection) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "was":{"type":"string","description":"The prior conclusion that's now wrong or incomplete. One sentence."},
  "now":{"type":"string","description":"The corrected conclusion. One sentence."},
  "why":{"type":"string","description":"Optional: short explanation of the trigger (test failure, user correction, new evidence)."}
},
"required":["was","now"]
}`)
}

func (r *RecordCorrection) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Was string `json:"was"`
		Now string `json:"now"`
		Why string `json:"why"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Was) == "" || strings.TrimSpace(p.Now) == "" {
		return "", errors.New("both 'was' and 'now' are required")
	}
	entry := Correction{At: time.Now(), Was: p.Was, Now: p.Now, Why: p.Why}
	if err := r.append(entry); err != nil {
		return "", err
	}
	return fmt.Sprintf("recorded correction: %s → %s", clipS(p.Was, 80), clipS(p.Now, 80)), nil
}

func (r *RecordCorrection) append(entry Correction) error {
	if r.Path == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil { //nolint:gosec
		return err
	}
	f, err := os.OpenFile(r.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// LoadCorrections reads the most-recent N entries from the session's
// corrections.jsonl. Used by the master to render an "<active corrections>"
// block in its tail context.
func LoadCorrections(path string, max int) []Correction {
	if path == "" || max <= 0 {
		return nil
	}
	b, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	out := make([]Correction, 0, len(lines))
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		var c Correction
		if json.Unmarshal([]byte(ln), &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

func clipS(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
