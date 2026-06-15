package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type TransactionalEdit struct{ Tracker *ChangeTracker }

func (TransactionalEdit) Name() string { return "transactional_edit" }
func (TransactionalEdit) Description() string {
	return `Prepare a coordinated multi-file change set, validate every precondition first, then apply the whole set together. If validate_command fails after writing, all touched files are rolled back to their pre-edit state. Use this for refactors that span multiple files and must not leave partial results behind.`
}
func (TransactionalEdit) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "changes":{
    "type":"array",
    "items":{
      "type":"object",
      "properties":{
        "path":{"type":"string"},
        "content":{"type":"string","description":"Whole-file replacement. Creates the file if it does not exist."},
        "edits":{
          "type":"array",
          "description":"Optional sequence of replacements applied in order to the current file content.",
          "items":{
            "type":"object",
            "properties":{
              "old_string":{"type":"string"},
              "new_string":{"type":"string"},
              "replace_all":{"type":"boolean"}
            },
            "required":["old_string","new_string"]
          }
        }
      },
      "required":["path"]
    },
    "minItems":1
  },
  "validate_command":{"type":"string","description":"Optional shell command to run after staging the change set. On failure, all touched files are rolled back."},
  "timeout_seconds":{"type":"integer","description":"Timeout for validate_command. Default 60, max 1800."}
},
"required":["changes"]
}`)
}

type transactionalChange struct {
	Path    string        `json:"path"`
	Content *string       `json:"content"`
	Edits   []multiEditOp `json:"edits"`
}

func (t TransactionalEdit) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Changes         []transactionalChange `json:"changes"`
		ValidateCommand string                `json:"validate_command"`
		TimeoutSeconds  int                   `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if len(p.Changes) == 0 {
		return "", errors.New("at least one change is required")
	}

	paths := make([]string, 0, len(p.Changes))
	for _, ch := range p.Changes {
		paths = append(paths, ch.Path)
	}
	unlock := GlobalLockManager.LockMany(paths)
	defer unlock()

	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 60
	}
	if p.TimeoutSeconds > 1800 {
		p.TimeoutSeconds = 1800
	}

	type prepared struct {
		path    string
		abs     string
		body    string
		kind    ChangeKind
		step    int
		mode    os.FileMode
		existed bool
	}
	preparedChanges := make([]prepared, 0, len(p.Changes))
	for i, ch := range p.Changes {
		if strings.TrimSpace(ch.Path) == "" {
			return "", fmt.Errorf("change %d: path is required", i+1)
		}
		abs, _ := filepath.Abs(ch.Path)
		current, err := os.ReadFile(ch.Path)
		existed := err == nil
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		mode := os.FileMode(0o644)
		if info, statErr := os.Stat(ch.Path); statErr == nil {
			mode = info.Mode().Perm()
		}
		body := string(current)
		switch {
		case ch.Content != nil:
			body = *ch.Content
		case len(ch.Edits) > 0:
			updated, editErr := applyMultiEdits(body, ch.Path, ch.Edits)
			if editErr != nil {
				return "", fmt.Errorf("change %d: %w", i+1, editErr)
			}
			body = updated
		default:
			return "", fmt.Errorf("change %d: provide either content or edits", i+1)
		}
		kind := ChangeCreated
		if existed {
			kind = ChangeEdited
		}
		preparedChanges = append(preparedChanges, prepared{
			path:    ch.Path,
			abs:     abs,
			body:    body,
			kind:    kind,
			mode:    mode,
			existed: existed,
		})
	}

	firstStep := 0
	for i := range preparedChanges {
		step := t.Tracker.BeginMutation(preparedChanges[i].abs)
		preparedChanges[i].step = step
		if firstStep == 0 || step < firstStep {
			firstStep = step
		}
		if err := writeAtomically(preparedChanges[i].path, []byte(preparedChanges[i].body), preparedChanges[i].mode); err != nil {
			if firstStep > 0 {
				_, _ = t.Tracker.Rewind(firstStep)
			}
			return "", err
		}
		t.Tracker.Record(Change{Path: preparedChanges[i].abs, Kind: preparedChanges[i].kind, Step: step})
	}

	if strings.TrimSpace(p.ValidateCommand) != "" {
		vctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
		defer cancel()
		cmd := exec.CommandContext(vctx, "bash", "-lc", p.ValidateCommand)
		out, err := cmd.CombinedOutput()
		if err != nil {
			if firstStep > 0 {
				_, _ = t.Tracker.Rewind(firstStep)
			}
			return "", fmt.Errorf("validate_command failed: %s", strings.TrimSpace(formatTestOutput(string(out), err)))
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "applied %d change(s)", len(preparedChanges))
	for _, ch := range preparedChanges {
		fmt.Fprintf(&sb, "\n- %s", ch.path)
		if lint := lintAfterEdit(ch.abs); lint != "" {
			sb.WriteString("\n  " + strings.ReplaceAll(lint, "\n", "\n  "))
		}
	}
	return sb.String(), nil
}

func applyMultiEdits(body, path string, edits []multiEditOp) (string, error) {
	for i, e := range edits {
		if e.OldString == "" {
			return "", fmt.Errorf("edit %d: old_string is empty", i+1)
		}
		count := strings.Count(body, e.OldString)
		switch {
		case count == 0:
			return "", fmt.Errorf("edit %d: %s", i+1, suggestApplyDiffForNoMatch(path))
		case e.ReplaceAll:
			body = strings.ReplaceAll(body, e.OldString, e.NewString)
		case count == 1:
			body = strings.Replace(body, e.OldString, e.NewString, 1)
		default:
			return "", fmt.Errorf("edit %d: %s", i+1, suggestApplyDiffForMultipleMatches(count, path))
		}
	}
	return body, nil
}

func writeAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".ageni-transaction-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
