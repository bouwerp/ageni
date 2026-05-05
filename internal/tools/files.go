package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadFile reads a file's contents, optionally a line range.
type ReadFile struct{}

func (ReadFile) Name() string { return "read_file" }
func (ReadFile) Description() string {
	return `Read a file's contents. Use offset+limit to read a specific line range — strongly recommended for files >500 lines. Returns a header showing the actual range and total file size.`
}
func (ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute or relative path to the file."},
  "offset":{"type":"integer","description":"1-indexed starting line. Default 1."},
  "limit":{"type":"integer","description":"Max lines to return. Default: whole file (capped at 2000 lines)."}
},
"required":["path"]
}`)
}
func (ReadFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	f, err := os.Open(p.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if p.Offset <= 0 {
		p.Offset = 1
	}
	if p.Limit <= 0 {
		p.Limit = 2000
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var sb strings.Builder
	totalLines := 0
	emitted := 0
	for scanner.Scan() {
		totalLines++
		if totalLines < p.Offset {
			continue
		}
		if emitted >= p.Limit {
			// keep counting to report total below
			continue
		}
		sb.WriteString(scanner.Text())
		sb.WriteByte('\n')
		emitted++
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	header := fmt.Sprintf("[%s lines %d-%d of %d]\n", p.Path, p.Offset, p.Offset+emitted-1, totalLines)
	if emitted == 0 {
		header = fmt.Sprintf("[%s: offset %d is past end of file (%d lines total)]\n", p.Path, p.Offset, totalLines)
	}
	return header + sb.String(), nil
}

// WriteFile writes (creating or overwriting) a file.
type WriteFile struct{ Tracker *ChangeTracker }

func (WriteFile) Name() string { return "write_file" }
func (WriteFile) Description() string {
	return `Write the full contents of a file. Use this for NEW files, or when you want to overwrite an existing file from scratch. Parent directories are created as needed.

For partial changes to an existing file, prefer edit_file (single replacement) or multi_edit (batch). Don't use write_file to "edit" — you'll lose any content you don't include.`
}
func (WriteFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)
}
func (w WriteFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path    string
		Content string
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	abs, _ := filepath.Abs(p.Path)
	existed := false
	if _, err := os.Stat(abs); err == nil {
		existed = true
	}
	step := w.Tracker.BeginMutation(abs)
	if dir := filepath.Dir(p.Path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
		return "", err
	}
	kind := ChangeCreated
	if existed {
		kind = ChangeEdited
	}
	w.Tracker.Record(Change{Path: abs, Kind: kind, Step: step})
	result := fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path)
	if lint := lintAfterEdit(abs); lint != "" {
		result += "\n" + lint
	}
	return result, nil
}

// EditFile does a single string replacement in a file. The old_string must
// occur exactly once for safety.
type EditFile struct{ Tracker *ChangeTracker }

func (EditFile) Name() string { return "edit_file" }
func (EditFile) Description() string {
	return `Replace exactly one occurrence of old_string with new_string in an EXISTING file. Fails if old_string is missing or appears more than once. Use this for targeted edits instead of rewriting the whole file.

Cannot create new files — use write_file for that.`
}
func (EditFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["path","old_string","new_string"]}`)
}
func (e EditFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path      string
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	b, err := os.ReadFile(p.Path)
	if err != nil {
		return "", err
	}
	body := string(b)
	count := strings.Count(body, p.OldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", p.Path)
	}
	if count > 1 {
		return "", fmt.Errorf("old_string occurs %d times in %s; provide more context to make it unique", count, p.Path)
	}
	abs, _ := filepath.Abs(p.Path)
	step := e.Tracker.BeginMutation(abs)
	updated := strings.Replace(body, p.OldString, p.NewString, 1)
	if err := os.WriteFile(p.Path, []byte(updated), 0o644); err != nil {
		return "", err
	}
	e.Tracker.Record(Change{Path: abs, Kind: ChangeEdited, Step: step})
	result := fmt.Sprintf("replaced 1 occurrence in %s", p.Path)
	if lint := lintAfterEdit(abs); lint != "" {
		result += "\n" + lint
	}
	return result, nil
}

// ListDir lists the entries in a directory.
type ListDir struct{}

func (ListDir) Name() string { return "list_dir" }
func (ListDir) Description() string {
	return "List entries in a directory. Returns files and subdirectories with type indicators."
}
func (ListDir) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory path; defaults to current working directory"}},"required":[]}`)
}
func (ListDir) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ Path string }
	_ = json.Unmarshal(args, &p)
	if p.Path == "" {
		p.Path = "."
	}
	entries, err := os.ReadDir(p.Path)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		names = append(names, e.Name()+suffix)
	}
	sort.Strings(names)
	return strings.Join(names, "\n"), nil
}
