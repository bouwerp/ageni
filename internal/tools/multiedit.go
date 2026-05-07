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
			var sb strings.Builder
			fmt.Fprintf(&sb, "edit %d: old_string not found in %s.\n", i+1, p.Path)
			candidates := fuzzyCandidates(body, e.OldString, 3)
			if len(candidates) == 0 {
				sb.WriteString("No similar regions found. Check whitespace and that the file has not changed since you last read it.")
			} else {
				sb.WriteString("Closest candidates in the current file:\n")
				for _, c := range candidates {
					fmt.Fprintf(&sb, "\n— lines %d-%d (overlap %d/%d):\n", c.startLine, c.endLine, c.overlap, c.want)
					for j, ln := range c.lines {
						fmt.Fprintf(&sb, "    %4d │ %s\n", c.startLine+j, ln)
					}
				}
				sb.WriteString("\nFix old_string to match exactly (whitespace counts), then retry.")
			}
			return "", errors.New(sb.String())
		case e.ReplaceAll:
			body = strings.ReplaceAll(body, e.OldString, e.NewString)
		case count == 1:
			body = strings.Replace(body, e.OldString, e.NewString, 1)
		default:
			return "", fmt.Errorf("edit %d: old_string occurs %d times; provide more context or set replace_all", i+1, count)
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
