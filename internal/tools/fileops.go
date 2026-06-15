package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MakeDir creates a directory and any missing parents.
type MakeDir struct{ Tracker *ChangeTracker }

func (MakeDir) Name() string { return "make_dir" }
func (MakeDir) Description() string {
	return "Create a directory (and any missing parent directories). No-op if the directory already exists."
}
func (MakeDir) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}
func (md MakeDir) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ Path string }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		p.Path = resolvePath(args)
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	abs, _ := filepath.Abs(p.Path)
	existed := false
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		existed = true
	}
	if err := os.MkdirAll(p.Path, 0o755); err != nil { //nolint:gosec
		return "", err
	}
	if !existed {
		step := md.Tracker.BeginMutation(abs)
		md.Tracker.Record(Change{Path: abs, Kind: ChangeMkdir, Step: step})
	}
	return fmt.Sprintf("created %s", p.Path), nil
}

// MoveFile renames or moves a file (or directory) atomically when on the
// same filesystem.
type MoveFile struct{ Tracker *ChangeTracker }

func (MoveFile) Name() string { return "move_file" }
func (MoveFile) Description() string {
	return "Rename or move a file (or directory) from src to dst. Fails if dst already exists unless overwrite=true. Parent of dst is created as needed."
}
func (MoveFile) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "src":{"type":"string"},
  "dst":{"type":"string"},
  "overwrite":{"type":"boolean","description":"If true, replace dst if it exists. Default false."}
},
"required":["src","dst"]
}`)
}
func (mv MoveFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Src       string `json:"src"`
		Dst       string `json:"dst"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Src == "" || p.Dst == "" {
		return "", errors.New("src and dst are required")
	}
	if _, err := os.Stat(p.Src); err != nil {
		return "", fmt.Errorf("src not found: %w", err)
	}
	if _, err := os.Stat(p.Dst); err == nil {
		if !p.Overwrite {
			return "", fmt.Errorf("dst exists; pass overwrite=true to replace: %s", p.Dst)
		}
		if err := os.RemoveAll(p.Dst); err != nil {
			return "", err
		}
	}
	srcAbs, _ := filepath.Abs(p.Src)
	dstAbs, _ := filepath.Abs(p.Dst)
	step := mv.Tracker.BeginMutation(srcAbs)
	if dir := filepath.Dir(p.Dst); dir != "" {
		_ = os.MkdirAll(dir, 0o755) //nolint:gosec
	}
	if err := os.Rename(p.Src, p.Dst); err != nil {
		return "", err
	}
	mv.Tracker.Record(Change{Path: dstAbs, Kind: ChangeMoved, From: srcAbs, Step: step})
	return fmt.Sprintf("moved %s -> %s", p.Src, p.Dst), nil
}

// DeleteFile removes a file. Refuses directories unless recursive=true to
// keep accidental wipes from happening.
type DeleteFile struct{ Tracker *ChangeTracker }

func (DeleteFile) Name() string { return "delete_file" }
func (DeleteFile) Description() string {
	return "Delete a file. To delete a directory, pass recursive=true (this will remove every file under it — use with care)."
}
func (DeleteFile) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string"},
  "recursive":{"type":"boolean","description":"Required when path is a directory. Removes all contents."}
},
"required":["path"]
}`)
}
func (d DeleteFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		p.Path = resolvePath(args)
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	info, err := os.Stat(p.Path)
	if err != nil {
		return "", fmt.Errorf("path not found: %w", err)
	}
	if info.IsDir() && !p.Recursive {
		return "", fmt.Errorf("%s is a directory; pass recursive=true to delete it and its contents", p.Path)
	}
	abs, _ := filepath.Abs(p.Path)
	step := d.Tracker.BeginMutation(abs)
	if p.Recursive {
		if err := os.RemoveAll(p.Path); err != nil {
			return "", err
		}
	} else {
		if err := os.Remove(p.Path); err != nil {
			return "", err
		}
	}
	d.Tracker.Record(Change{Path: abs, Kind: ChangeDeleted, Step: step})
	return fmt.Sprintf("deleted %s", p.Path), nil
}
