package tools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ReadFile reads a file's contents, optionally a line range.
type ReadFile struct{ Cache *ReadFileCache }

type ReadFileCache struct {
	mu      sync.Mutex
	nextSeq int
	entries map[string]readFileFingerprint
}

type readFileFingerprint struct {
	Hash        [32]byte
	FirstReadID int
}

func NewReadFileCache() *ReadFileCache {
	return &ReadFileCache{entries: make(map[string]readFileFingerprint)}
}

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
  "limit":{"type":"integer","description":"Max lines to return. Default: whole file (capped at 500 lines)."}
},
"required":["path"]
}`)
}
func (r ReadFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
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

	// Resolve alternative names / aliases for Offset and Limit
	var extra struct {
		StartLine  int `json:"StartLine"`
		EndLine    int `json:"EndLine"`
		StartLine2 int `json:"start_line"`
		EndLine2   int `json:"end_line"`
		StartLine3 int `json:"startLine"`
		EndLine3   int `json:"endLine"`
		Offset2    int `json:"Offset"`
		Limit2     int `json:"Limit"`
	}
	if err := json.Unmarshal(args, &extra); err == nil {
		if p.Offset <= 0 {
			if extra.Offset2 > 0 {
				p.Offset = extra.Offset2
			} else if extra.StartLine > 0 {
				p.Offset = extra.StartLine
			} else if extra.StartLine2 > 0 {
				p.Offset = extra.StartLine2
			} else if extra.StartLine3 > 0 {
				p.Offset = extra.StartLine3
			}
		}
		if p.Limit <= 0 {
			if extra.Limit2 > 0 {
				p.Limit = extra.Limit2
			} else {
				start := p.Offset
				if start <= 0 {
					start = 1
				}
				end := 0
				if extra.EndLine > 0 {
					end = extra.EndLine
				} else if extra.EndLine2 > 0 {
					end = extra.EndLine2
				} else if extra.EndLine3 > 0 {
					end = extra.EndLine3
				}
				if end >= start {
					p.Limit = end - start + 1
				}
			}
		}
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
		p.Limit = 500
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
	content := sb.String()
	if stub, ok := r.cachedReadStub(p.Path, p.Offset, p.Limit, emitted, totalLines, content); ok {
		return stub, nil
	}
	return header + content, nil
}

func (r ReadFile) cachedReadStub(path string, offset, limit, emitted, totalLines int, content string) (string, bool) {
	if r.Cache == nil {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	key := fmt.Sprintf("%s:%d:%d", abs, offset, limit)
	hash := sha256.Sum256([]byte(content))

	r.Cache.mu.Lock()
	defer r.Cache.mu.Unlock()
	if r.Cache.entries == nil {
		r.Cache.entries = make(map[string]readFileFingerprint)
	}
	if prior, ok := r.Cache.entries[key]; ok && prior.Hash == hash {
		end := offset + emitted - 1
		if emitted == 0 {
			return fmt.Sprintf("[%s already read as request #%d; offset %d is still past end of file (%d lines total)]\n", path, prior.FirstReadID, offset, totalLines), true
		}
		return fmt.Sprintf("[%s already read as request #%d lines %d-%d of %d; content unchanged]\n", path, prior.FirstReadID, offset, end, totalLines), true
	}
	r.Cache.nextSeq++
	r.Cache.entries[key] = readFileFingerprint{
		Hash:        hash,
		FirstReadID: r.Cache.nextSeq,
	}
	return "", false
}

// WriteFile writes (creating or overwriting) a file.
type WriteFile struct{ Tracker *ChangeTracker }

func (WriteFile) Name() string { return "write_file" }
func (WriteFile) Description() string {
	return `Write the full contents of a file. Use this for NEW files, or when you want to overwrite an existing file from scratch. Parent directories are created as needed.

For partial changes to an existing file, prefer apply_diff for complex edits, or edit_file / multi_edit for exact-match replacements. Don't use write_file to "edit" — you'll lose any content you don't include.`
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
		p.Path = ResolvePath(args)
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
	return `Replace exactly one occurrence of old_string with new_string in an EXISTING file. Fails if old_string is missing or appears more than once. Use this only for exact one-off replacements.

For multi-line, multi-block, or approximate edits, prefer apply_diff.

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
		p.Path = ResolvePath(args)
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
		return "", errors.New(suggestApplyDiffForNoMatch(p.Path))
	}
	if count > 1 {
		return "", errors.New(suggestApplyDiffForMultipleMatches(count, p.Path))
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
		fullName := e.Name()
		if p.Path != "." && p.Path != "" {
			fullName = filepath.Join(p.Path, e.Name())
		}
		names = append(names, fullName+suffix)
	}
	sort.Strings(names)
	return strings.Join(names, "\n"), nil
}
