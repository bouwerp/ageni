package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MultiEdit applies a sequence of string replacements atomically. Each edit's
// old_string must occur exactly once at the time it's applied; if any fail
// the file is left untouched.
type MultiEdit struct{ Tracker *ChangeTracker }

func (MultiEdit) Name() string { return "multi_edit" }
func (MultiEdit) Description() string {
	return `Apply multiple edits to an EXISTING file atomically. Each edit replaces one occurrence of old_string with new_string. Edits are applied in order; later edits see earlier edits' results. Set replace_all=true on an edit to replace every occurrence (otherwise old_string must occur exactly once). The file is only written if ALL edits succeed.

Use this for deterministic exact-match batches. For larger code reshapes or fuzzy multi-line edits, prefer apply_diff.

Cannot create new files — for new files use write_file. To prepend to a file, read the current contents and pass the whole new body to write_file.`
}
func (MultiEdit) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string"},
  "edits":{
    "type":"array",
    "description":"Sequence of edits applied in order.",
    "items":{
      "type":"object",
      "properties":{
        "old_string":{"type":"string"},
        "new_string":{"type":"string"},
        "replace_all":{"type":"boolean","description":"Replace all occurrences. Default false (must occur exactly once)."}
      },
      "required":["old_string","new_string"]
    },
    "minItems":1
  }
},
"required":["path","edits"]
}`)
}

type multiEditOp struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (m MultiEdit) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path  string        `json:"path"`
		Edits []multiEditOp `json:"edits"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		p.Path = ResolvePath(args)
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	if len(p.Edits) == 0 {
		return "", errors.New("at least one edit is required")
	}
	b, err := os.ReadFile(p.Path)
	if err != nil {
		return "", err
	}
	body := string(b)
	applied := 0
	for i, e := range p.Edits {
		if e.OldString == "" {
			return "", fmt.Errorf("edit %d: old_string is empty", i+1)
		}
		count := strings.Count(body, e.OldString)
		switch {
		case count == 0:
			return "", fmt.Errorf("edit %d: %s", i+1, suggestApplyDiffForNoMatch(p.Path))
		case e.ReplaceAll:
			body = strings.ReplaceAll(body, e.OldString, e.NewString)
		case count == 1:
			body = strings.Replace(body, e.OldString, e.NewString, 1)
		default:
			return "", fmt.Errorf("edit %d: %s", i+1, suggestApplyDiffForMultipleMatches(count, p.Path))
		}
		applied++
	}
	abs, _ := filepath.Abs(p.Path)
	step := m.Tracker.BeginMutation(abs)
	if err := os.WriteFile(p.Path, []byte(body), 0o644); err != nil { //nolint:gosec
		return "", err
	}
	m.Tracker.Record(Change{Path: abs, Kind: ChangeEdited, Step: step})
	result := fmt.Sprintf("applied %d edits to %s", applied, p.Path)
	if lint := lintAfterEdit(abs); lint != "" {
		result += "\n" + lint
	}
	return result, nil
}
