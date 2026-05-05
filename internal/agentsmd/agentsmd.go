// Package agentsmd loads AGENTS.md project-instruction files using the
// emerging cross-vendor convention adopted by Codex, Cursor, Amp,
// Factory, Jules, and GitHub Copilot. Documented at https://agents.md.
//
// Lookup walks from the repo root downward, concatenating every
// AGENTS.md it finds with a path header so the master knows which
// directory each block applies to. Nearest-wins is enforced by the
// master prompt itself (instructions for deeper paths come later in
// the rendered block, and the prompt tells the model to prefer
// later-mentioned scope when there's a conflict).
package agentsmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Filename is the canonical instruction file name. agents.md (lowercase)
// is also accepted to match the original convention casing flexibility,
// but AGENTS.md is the recommended form.
var candidateNames = []string{"AGENTS.md", "agents.md"}

// LoadResult is what Load returns: the rendered block ready to inject
// into a system prompt, plus the list of source paths so callers can
// log where the content came from.
type LoadResult struct {
	Rendered string
	Paths    []string
}

// Load walks rootDir recursively, collecting every AGENTS.md file, and
// returns a single rendered block. Returns a zero-value LoadResult when
// no files are found. Skips common vendored / generated dirs to avoid
// pulling in third-party AGENTS.md files that don't apply to the user's
// project.
//
// Files are read with a 256KB cap each to prevent runaway content from
// blowing up the system prompt; oversize files are truncated with a
// trailing marker.
func Load(rootDir string) (LoadResult, error) {
	if rootDir == "" {
		return LoadResult{}, nil
	}
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return LoadResult{}, err
	}
	var paths []string
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Stat / permission errors on individual entries shouldn't
			// abort the whole walk — skip and continue.
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) && path != abs {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		for _, candidate := range candidateNames {
			if name == candidate {
				paths = append(paths, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		return LoadResult{}, err
	}
	if len(paths) == 0 {
		return LoadResult{}, nil
	}
	// Sort by path depth then lexically so root AGENTS.md comes first
	// and nested ones follow. The prompt convention is "later overrides
	// for paths under that scope".
	sort.SliceStable(paths, func(i, j int) bool {
		di := strings.Count(paths[i], string(filepath.Separator))
		dj := strings.Count(paths[j], string(filepath.Separator))
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})

	var sb strings.Builder
	for _, p := range paths {
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			rel = p
		}
		scope := filepath.Dir(rel)
		if scope == "." {
			scope = "(repo root)"
		}
		body, err := readCapped(p, 256*1024)
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "<agents_md scope=%q>\n%s\n</agents_md>\n\n", scope, strings.TrimSpace(body))
	}
	return LoadResult{Rendered: strings.TrimRight(sb.String(), "\n"), Paths: paths}, nil
}

// readCapped reads up to maxBytes from path, returning a truncation
// marker if the file exceeds the cap. Prevents a stray multi-megabyte
// AGENTS.md from blowing up the system prompt.
func readCapped(path string, maxBytes int64) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return "", err
	}
	if int64(len(data)) <= maxBytes {
		return string(data), nil
	}
	return string(data[:maxBytes]) + fmt.Sprintf("\n…(truncated at %d bytes)", maxBytes), nil
}

// shouldSkipDir is the deny-list of directory names we never descend
// into when looking for AGENTS.md. These are dirs that commonly contain
// vendored / generated content with their own (irrelevant) AGENTS.md
// files.
func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn",
		"node_modules", "vendor", "third_party", "third-party",
		"target", "build", "dist", "out",
		".venv", "venv", "__pycache__",
		".cache", ".tox", ".pytest_cache":
		return true
	}
	return false
}
