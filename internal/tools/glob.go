package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Glob finds files by glob pattern with ** support.
type Glob struct{}

func (Glob) Name() string { return "glob" }
func (Glob) Description() string {
	return `Find files matching a glob pattern (supports ** for recursive). Examples: '**/*.go', 'cmd/**/main.go', 'src/*.{ts,tsx}'. Returns up to 200 paths sorted by name.`
}
func (Glob) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "pattern":{"type":"string","description":"Glob pattern with ** support."},
  "path":{"type":"string","description":"Root directory. Defaults to cwd."},
  "max_results":{"type":"integer","description":"Cap on returned paths. Default 200, max 1000."}
},
"required":["pattern"]
}`)
}
func (Glob) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Pattern == "" {
		p.Pattern = ResolveQuery(args)
	}
	if p.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	if p.Path == "" {
		p.Path = "."
	}
	validatedPath, err := ValidatePath(p.Path)
	if err != nil {
		return "", err
	}
	p.Path = validatedPath
	if p.MaxResults <= 0 {
		p.MaxResults = 200
	}
	if p.MaxResults > 1000 {
		p.MaxResults = 1000
	}

	root := os.DirFS(p.Path)
	var matches []string
	err = doublestar.GlobWalk(root, p.Pattern, func(path string, d fs.DirEntry) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			// Skip common heavy directories.
			base := d.Name()
			if base == "node_modules" || base == ".git" || base == "vendor" || base == "dist" || base == "build" || base == ".venv" {
				return fs.SkipDir
			}
			return nil
		}
		full := filepath.Join(p.Path, path)
		matches = append(matches, full)
		if len(matches) >= p.MaxResults {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "", err
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	out := strings.Join(matches, "\n")
	if len(matches) >= p.MaxResults {
		out += fmt.Sprintf("\n[truncated to %d matches]", p.MaxResults)
	}
	return out, nil
}
