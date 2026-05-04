package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

// GitStatus shows the working tree status using porcelain v2 (structured).
type GitStatus struct{}

func (GitStatus) Name() string { return "git_status" }
func (GitStatus) Description() string {
	return `Show git working tree status. Lists modified, added, deleted, untracked, and renamed files. Returns parsed output, not raw porcelain.`
}
func (GitStatus) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Repo path. Defaults to cwd."}},"required":[]}`)
}
func (GitStatus) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireCLI("git"); err != nil {
		return "", err
	}
	var p struct{ Path string }
	_ = json.Unmarshal(args, &p)

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v2", "--branch", "-z")
	if p.Path != "" {
		cmd.Dir = p.Path
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}

	var sb strings.Builder
	parts := bytes.Split(out, []byte{0})
	for _, raw := range parts {
		line := string(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch."):
			sb.WriteString(line + "\n")
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			// "1 XY sub mH mI mW hH hI path" — XY is the change code.
			fields := strings.SplitN(line, " ", 9)
			if len(fields) >= 9 {
				sb.WriteString(fmt.Sprintf("%s %s\n", fields[1], fields[8]))
			}
		case strings.HasPrefix(line, "? "):
			sb.WriteString("?? " + strings.TrimPrefix(line, "? ") + "\n")
		}
	}
	if sb.Len() == 0 {
		return "(working tree clean)", nil
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// GitDiff shows changes — staged, unstaged, or against a revision.
type GitDiff struct{}

func (GitDiff) Name() string { return "git_diff" }
func (GitDiff) Description() string {
	return `Show git diff. By default shows unstaged changes; staged=true for staged; revision="HEAD~1" for a comparison against an arbitrary ref. Output is the unified diff.`
}
func (GitDiff) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "staged":{"type":"boolean","description":"Show staged changes (--cached)."},
  "revision":{"type":"string","description":"Compare against this ref (e.g. HEAD, main, abc123)."},
  "path":{"type":"string","description":"Limit to a path (file or dir)."},
  "max_bytes":{"type":"integer","description":"Truncate. Default 16000."}
},
"required":[]
}`)
}
func (GitDiff) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireCLI("git"); err != nil {
		return "", err
	}
	var p struct {
		Staged   bool   `json:"staged"`
		Revision string `json:"revision"`
		Path     string `json:"path"`
		MaxBytes int    `json:"max_bytes"`
	}
	_ = json.Unmarshal(args, &p)
	if p.MaxBytes <= 0 {
		p.MaxBytes = 16000
	}

	cliArgs := []string{"diff", "--no-color"}
	if p.Staged {
		cliArgs = append(cliArgs, "--cached")
	}
	if p.Revision != "" {
		cliArgs = append(cliArgs, p.Revision)
	}
	if p.Path != "" {
		cliArgs = append(cliArgs, "--", p.Path)
	}
	cmd := exec.CommandContext(ctx, "git", cliArgs...)
	out, _ := cmd.Output()
	s := string(out)
	if s == "" {
		return "(no changes)", nil
	}
	if len(s) > p.MaxBytes {
		s = s[:p.MaxBytes] + fmt.Sprintf("\n[truncated to %d bytes]", p.MaxBytes)
	}
	return s, nil
}

// GitLog shows recent commits.
type GitLog struct{}

func (GitLog) Name() string { return "git_log" }
func (GitLog) Description() string {
	return `Show recent git commits with subject, author, and date. Optionally filter by path or revision range.`
}
func (GitLog) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "limit":{"type":"integer","description":"Max commits. Default 20, max 200."},
  "path":{"type":"string","description":"Limit to a path."},
  "revision_range":{"type":"string","description":"Range like 'main..HEAD' or 'abc123..'."}
},
"required":[]
}`)
}
func (GitLog) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireCLI("git"); err != nil {
		return "", err
	}
	var p struct {
		Limit         int    `json:"limit"`
		Path          string `json:"path"`
		RevisionRange string `json:"revision_range"`
	}
	_ = json.Unmarshal(args, &p)
	if p.Limit <= 0 {
		p.Limit = 20
	}
	if p.Limit > 200 {
		p.Limit = 200
	}

	cliArgs := []string{"log", "--pretty=format:%h  %ad  %an  %s", "--date=short", fmt.Sprintf("-n%d", p.Limit)}
	if p.RevisionRange != "" {
		cliArgs = append(cliArgs, p.RevisionRange)
	}
	if p.Path != "" {
		cliArgs = append(cliArgs, "--", p.Path)
	}
	cmd := exec.CommandContext(ctx, "git", cliArgs...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git log: %w", err)
	}
	if len(out) == 0 {
		return "(no commits)", nil
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// ComputeDiff returns a unified diff between two in-memory strings. Useful
// for previewing edits without writing to disk.
type ComputeDiff struct{}

func (ComputeDiff) Name() string { return "compute_diff" }
func (ComputeDiff) Description() string {
	return `Compute a unified diff between two strings (e.g. before/after of a planned edit). Both strings are required. Pass label_a/label_b for clearer headers.`
}
func (ComputeDiff) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "a":{"type":"string","description":"Original content."},
  "b":{"type":"string","description":"New content."},
  "label_a":{"type":"string","description":"Header label for a. Default 'a'."},
  "label_b":{"type":"string","description":"Header label for b. Default 'b'."}
},
"required":["a","b"]
}`)
}
func (ComputeDiff) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		A      string `json:"a"`
		B      string `json:"b"`
		LabelA string `json:"label_a"`
		LabelB string `json:"label_b"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.LabelA == "" {
		p.LabelA = "a"
	}
	if p.LabelB == "" {
		p.LabelB = "b"
	}
	edits := myers.ComputeEdits(span.URIFromPath(p.LabelA), p.A, p.B)
	diff := fmt.Sprint(gotextdiff.ToUnified(p.LabelA, p.LabelB, p.A, edits))
	if diff == "" {
		return "(identical)", nil
	}
	return diff, nil
}
