package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bouwerp/ageni/internal/homedir"
	"github.com/bouwerp/ageni/internal/tools"
)

// sensitivePathPatterns are path patterns that should never be read by an
// agent. Matches are case-insensitive and checked against the basename and
// the full path.
var sensitivePathPatterns = []string{
	".env",
	"identity.age",
	"secrets.age",
	"keyring",
	"id_rsa",
	"id_ed25519",
	"id_ecdsa",
	"id_dsa",
	".pem",
	".key",
	".p12",
	".pfx",
	"credentials",
	"secrets.json",
	"secrets.yaml",
	"secrets.yml",
}

// ageniDirs are directory names under ~/.ageni that are always blocked.
// Populated lazily on first use; uses homedir.Dir() which has a built-in
// timeout so it never blocks the sensitive-path check for long.
var (
	ageniDirsOnce sync.Once
	ageniDirs     []string
	cachedHome    string
)

func resolveAgeniDirs() {
	ageniDirsOnce.Do(func() {
		home, _ := homedir.Dir()
		cachedHome = home
		if home != "" {
			ageniDirs = []string{
				filepath.Join(home, ".ageni", ".env"),
				filepath.Join(home, ".ageni", "keyring"),
				filepath.Join(home, ".ageni", "identity.age"),
				filepath.Join(home, ".ageni", "secrets.age"),
			}
		}
	})
}

// isSensitivePath returns true if the path matches any known sensitive pattern.
func isSensitivePath(path string) bool {
	resolveAgeniDirs()
	// Normalize: resolve ~ and make absolute if possible.
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(cachedHome, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}

	lpath := strings.ToLower(path)
	lbase := strings.ToLower(filepath.Base(path))

	// Check known ageni dirs.
	for _, blocked := range ageniDirs {
		if strings.HasPrefix(path, blocked) {
			return true
		}
	}

	// Check basename and full path against patterns.
	for _, pat := range sensitivePathPatterns {
		if strings.Contains(lbase, pat) || strings.Contains(lpath, pat) {
			return true
		}
	}
	return false
}

// GuardedReadFile wraps tools.ReadFile and blocks reads of sensitive paths
// (secret files, private keys, encrypted vaults) before the file is opened.
// The block message explains what happened without revealing whether the file
// exists or its contents.
type GuardedReadFile struct {
	inner tools.ReadFile
}

// NewGuardedReadFile returns a ReadFile tool with path-blocking enabled.
func NewGuardedReadFile(cache *tools.ReadFileCache) GuardedReadFile {
	return GuardedReadFile{inner: tools.ReadFile{Cache: cache}}
}

func (GuardedReadFile) Name() string              { return "read_file" }
func (g GuardedReadFile) Description() string     { return g.inner.Description() }
func (g GuardedReadFile) Schema() json.RawMessage { return g.inner.Schema() }

func (g GuardedReadFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return g.inner.Call(ctx, args)
	}

	if isSensitivePath(p.Path) {
		return fmt.Sprintf(
			"[BLOCKED: %q is a sensitive path that agents are not permitted to read directly. "+
				"Use list_secrets to see available credentials, or run_with_secret to execute "+
				"commands that require them.]",
			filepath.Base(p.Path),
		), nil
	}

	return g.inner.Call(ctx, args)
}

// GuardedGrep wraps tools.Grep and blocks searches targeting sensitive paths.
type GuardedGrep struct {
	inner tools.Grep
}

func NewGuardedGrep() GuardedGrep { return GuardedGrep{} }

func (GuardedGrep) Name() string              { return "grep" }
func (g GuardedGrep) Description() string     { return g.inner.Description() }
func (g GuardedGrep) Schema() json.RawMessage { return g.inner.Schema() }

func (g GuardedGrep) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err == nil && isSensitivePath(p.Path) {
		return "[BLOCKED: search target is a sensitive path — use list_secrets instead.]", nil
	}
	return g.inner.Call(ctx, args)
}
