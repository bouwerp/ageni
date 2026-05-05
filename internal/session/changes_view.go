package session

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/ageni/internal/tools"
)

// FormatChanges writes a human-readable summary of the session's recorded
// file changes to w. One line per distinct path with kind + relative path
// + age. Used by `ageni sessions changes`.
func FormatChanges(s *Session, w io.Writer) error {
	tr := tools.NewChangeTracker(s.Path("changes.jsonl"), s.Path("snapshots"))
	items := tr.Summary()
	if len(items) == 0 {
		fmt.Fprintln(w, "(no recorded changes in this session)")
		return nil
	}
	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()
	cwd, _ := os.Getwd()
	for _, c := range items {
		display := c.Path
		if cwd != "" {
			if rel, err := filepath.Rel(cwd, c.Path); err == nil && !strings.HasPrefix(rel, "..") {
				display = rel
			}
		}
		fmt.Fprintf(bw, "%-8s  %s  %s\n", c.Kind, humanise(c.At), display)
		if c.Kind == tools.ChangeMoved && c.From != "" {
			fmt.Fprintf(bw, "%-8s    from %s\n", "", c.From)
		}
	}
	return nil
}

// FormatDiff writes a unified diff for the session's changes. If path is
// empty, every changed path is diffed; otherwise only the matching path.
// Snapshot is the baseline (file content the first time it was touched);
// current state is read from disk. Files that no longer exist (deleted)
// produce a deletion diff against the snapshot.
func FormatDiff(s *Session, path string, w io.Writer) error {
	tr := tools.NewChangeTracker(s.Path("changes.jsonl"), s.Path("snapshots"))
	summary := tr.Summary()
	if len(summary) == 0 {
		fmt.Fprintln(w, "(no recorded changes in this session)")
		return nil
	}

	targets := summary
	if path != "" {
		resolved := tr.ResolvePath(path)
		if resolved == "" {
			return fmt.Errorf("no recorded change matches %q", path)
		}
		filtered := make([]tools.Change, 0, 1)
		for _, c := range summary {
			if c.Path == resolved {
				filtered = append(filtered, c)
				break
			}
		}
		targets = filtered
	}

	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()

	for _, c := range targets {
		if err := writeOneDiff(bw, tr, c); err != nil {
			fmt.Fprintf(bw, "diff error for %s: %v\n", c.Path, err)
		}
	}
	return nil
}

// writeOneDiff renders the unified diff for one change entry. We prefer
// `diff -u` (universally available, deterministic output); the snapshot
// is the baseline and the working file is the current state. For mkdir
// entries there's nothing to diff.
func writeOneDiff(w io.Writer, tr *tools.ChangeTracker, c tools.Change) error {
	if c.Kind == tools.ChangeMkdir {
		fmt.Fprintf(w, "--- (mkdir) %s\n\n", c.Path)
		return nil
	}
	snap := tr.SnapshotPath(c.Path)
	if _, err := os.Stat(snap); err != nil {
		return fmt.Errorf("no snapshot for %s", c.Path)
	}
	current := c.Path
	if c.Kind == tools.ChangeDeleted {
		// File gone — diff snapshot against /dev/null
		current = os.DevNull
	}
	// Args are constants + paths derived from the session's own change
	// log; the binary is the platform `diff`. This is the documented use.
	cmd := exec.Command("diff", "-u", //nolint:gosec
		"--label", "a/"+displayPath(c.Path),
		"--label", "b/"+displayPath(c.Path),
		snap, current,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	// `diff` exits 1 when files differ — that's the success path for us.
	var exitErr *exec.ExitError
	if err != nil && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		err = nil
	}
	if err != nil {
		return err
	}
	if out.Len() == 0 {
		// No textual diff (binary, identical, or untracked). Note it.
		fmt.Fprintf(w, "--- %s (no diff)\n\n", displayPath(c.Path))
		return nil
	}
	_, _ = w.Write(out.Bytes())
	if !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
	return nil
}

func displayPath(abs string) string {
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return abs
}

func humanise(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}
