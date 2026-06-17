package tools

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// lintAfterEdit runs a language-appropriate linter on the file just
// modified and returns a concise summary suitable for appending to a
// tool's return string. Returns "" when no linter is configured for the
// file's language or the linter isn't installed.
//
// The master / worker sees the appended summary and can decide on its
// own whether to follow up with a fix. We do NOT auto-mutate — that
// would surprise users and produce diffs they didn't ask for.
//
// Each linter is capped at 8 seconds to prevent a runaway type-check
// from stalling the agent loop.
func lintAfterEdit(absPath string) string {
	if absPath == "" {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(absPath))
	switch ext {
	case ".go":
		return goLint(absPath)
	case ".py":
		return pyLint(absPath)
	case ".ts", ".tsx":
		return tsLint(absPath)
	case ".js", ".jsx", ".mjs", ".cjs":
		return jsLint(absPath)
	case ".rs":
		return rustLint(absPath)
	case ".java":
		return javaLint(absPath)
	}
	return ""
}

// goLint runs gofmt -l on the single file. Empty output = formatted
// correctly. Non-empty = the file would be reformatted; we return a
// diff (gofmt -d) so the model sees exactly what to fix.
func goLint(path string) string {
	if _, err := exec.LookPath("gofmt"); err != nil {
		return ""
	}
	out, err := runLinter("gofmt", []string{"-l", path}, 8*time.Second)
	if err != nil {
		return ""
	}
	if strings.TrimSpace(out) == "" {
		return "[lint] gofmt: ok"
	}
	// Reformat needed — fetch the diff for the master to act on.
	diff, _ := runLinter("gofmt", []string{"-d", path}, 8*time.Second)
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return "[lint] gofmt: would reformat " + path
	}
	if len(diff) > 4000 {
		diff = diff[:4000] + "\n…(truncated)"
	}
	return "[lint] gofmt: file needs reformat (run `gofmt -w " + path + "`):\n" + diff
}

// pyLint runs ruff check on the single file when ruff is installed.
// Uses --exit-zero so we always get the report regardless of severity.
func pyLint(path string) string {
	if _, err := exec.LookPath("ruff"); err != nil {
		return ""
	}
	out, err := runLinter("ruff", []string{"check", "--exit-zero", path}, 8*time.Second)
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "[lint] ruff: ok"
	}
	if len(out) > 4000 {
		out = out[:4000] + "\n…(truncated)"
	}
	return "[lint] ruff:\n" + out
}

func tsLint(path string) string {
	if out := biomeLint(path); out != "" {
		return out
	}
	if !fileExists("tsconfig.json") && !fileExists("tsconfig.base.json") {
		return ""
	}
	if bin, args := resolveLocalOrGlobal("tsc"); bin != "" {
		out, err := runLinter(bin, append(args, "--noEmit", "--pretty", "false"), 20*time.Second)
		if err != nil {
			return ""
		}
		out = strings.TrimSpace(out)
		if out == "" {
			return "[lint] tsc: ok"
		}
		if len(out) > 4000 {
			out = out[:4000] + "\n…(truncated)"
		}
		return "[lint] tsc:\n" + out
	}
	return ""
}

func jsLint(path string) string {
	if out := biomeLint(path); out != "" {
		return out
	}
	if bin, args := resolveLocalOrGlobal("eslint"); bin != "" {
		out, err := runLinter(bin, append(args, "--format", "unix", path), 12*time.Second)
		if err != nil {
			return ""
		}
		out = strings.TrimSpace(out)
		if out == "" {
			return "[lint] eslint: ok"
		}
		if len(out) > 4000 {
			out = out[:4000] + "\n…(truncated)"
		}
		return "[lint] eslint:\n" + out
	}
	return ""
}

func biomeLint(path string) string {
	if bin, args := resolveLocalOrGlobal("biome"); bin != "" {
		out, err := runLinter(bin, append(args, "check", "--formatter-enabled=false", path), 12*time.Second)
		if err != nil {
			return ""
		}
		out = strings.TrimSpace(out)
		if out == "" {
			return "[lint] biome: ok"
		}
		if len(out) > 4000 {
			out = out[:4000] + "\n…(truncated)"
		}
		return "[lint] biome:\n" + out
	}
	return ""
}

func rustLint(path string) string {
	if !fileExists("Cargo.toml") {
		return ""
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		return ""
	}
	out, err := runLinter("cargo", []string{"check", "--quiet"}, 30*time.Second)
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "[lint] cargo check: ok"
	}
	if len(out) > 4000 {
		out = out[:4000] + "\n…(truncated)"
	}
	return "[lint] cargo check:\n" + out
}

func javaLint(path string) string {
	switch {
	case fileExists("pom.xml"):
		if _, err := exec.LookPath("mvn"); err != nil {
			return ""
		}
		out, err := runLinter("mvn", []string{"-q", "-DskipTests", "compile"}, 30*time.Second)
		if err != nil {
			return ""
		}
		out = strings.TrimSpace(out)
		if out == "" {
			return "[lint] mvn compile: ok"
		}
		if len(out) > 4000 {
			out = out[:4000] + "\n…(truncated)"
		}
		return "[lint] mvn compile:\n" + out
	case fileExists("gradlew"):
		out, err := runLinter("./gradlew", []string{"-q", "classes"}, 30*time.Second)
		if err != nil {
			return ""
		}
		out = strings.TrimSpace(out)
		if out == "" {
			return "[lint] gradle classes: ok"
		}
		if len(out) > 4000 {
			out = out[:4000] + "\n…(truncated)"
		}
		return "[lint] gradle classes:\n" + out
	}
	return ""
}

func resolveLocalOrGlobal(name string) (string, []string) {
	local := filepath.Join("node_modules", ".bin", name)
	if _, err := exec.LookPath(local); err == nil {
		return local, nil
	}
	if global, err := exec.LookPath(name); err == nil {
		return global, nil
	}
	return "", nil
}

// runLinter executes a linter with a hard timeout, returning combined
// stdout+stderr. Linters typically exit nonzero when issues are found —
// that's not an error to us; we want the report.
func runLinter(name string, args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec
	out, err := cmd.CombinedOutput()
	res := string(out)
	if err != nil && res != "" {
		res = EnrichErrorContext(res)
	}
	return res, nil
}
