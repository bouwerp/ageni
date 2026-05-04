package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadFile reads a file's contents.
type ReadFile struct{}

func (ReadFile) Name() string { return "read_file" }
func (ReadFile) Description() string {
	return "Read a file's full contents. Use this to inspect source code, configs, or any text file."
}
func (ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute or relative path to the file"}},"required":["path"]}`)
}
func (ReadFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ Path string }
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
	return string(b), nil
}

// WriteFile writes (creating or overwriting) a file.
type WriteFile struct{}

func (WriteFile) Name() string { return "write_file" }
func (WriteFile) Description() string {
	return "Write a file with the given contents. Creates the file if it doesn't exist; overwrites if it does. Use edit_file for partial updates."
}
func (WriteFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)
}
func (WriteFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
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
	if dir := filepath.Dir(p.Path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
}

// EditFile does a single string replacement in a file. The old_string must
// occur exactly once for safety.
type EditFile struct{}

func (EditFile) Name() string { return "edit_file" }
func (EditFile) Description() string {
	return "Replace exactly one occurrence of old_string with new_string in a file. Fails if old_string is missing or not unique. Use this for targeted edits instead of rewriting the whole file."
}
func (EditFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["path","old_string","new_string"]}`)
}
func (EditFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
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
	updated := strings.Replace(body, p.OldString, p.NewString, 1)
	if err := os.WriteFile(p.Path, []byte(updated), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced 1 occurrence in %s", p.Path), nil
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
