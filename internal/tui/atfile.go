package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// atRefRegex matches @<path> tokens at the start of input or after
// whitespace. The path is a run of word chars, dots, slashes, dashes,
// underscores — covering relative paths like @internal/agent/master.go,
// dotfiles like @.gitignore, and bare names like @README.md while
// excluding constructs that aren't file refs (email addresses, twitter
// handles mid-sentence — those have non-whitespace immediately before @).
var atRefRegex = regexp.MustCompile(`(?:^|\s)@([\w./\-]+)`)

// maxAttachedFileBytes caps each @-mentioned file at 200KB. Larger files
// would bloat the master's context and slow inference; the user can
// always paste a snippet manually if they need a specific section of a
// huge file.
const maxAttachedFileBytes = 200 * 1024

// expandFileMentions scans text for @<path> tokens, attempts to read
// each as an existing readable file, and returns:
//
//   - expanded: the original text followed by one <attached_file
//     path="…">…</attached_file> block per successfully-attached file
//   - attachedPaths: the paths that actually got attached (for showing
//     the user a one-line confirmation in the flash bar)
//   - skipped: paths matched as @-tokens but not attachable, with a
//     reason — non-existent, directory, too large, unreadable
//
// The original @<path> tokens stay in the user's prompt verbatim so the
// model has the textual reference, and the file content arrives via the
// trailing structured blocks.
func expandFileMentions(text string) (expanded string, attachedPaths, skipped []string) {
	matches := atRefRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return text, nil, nil
	}
	seen := map[string]bool{}
	var attachments strings.Builder
	for _, m := range matches {
		path := m[1]
		if seen[path] {
			continue
		}
		seen[path] = true

		absPath := path
		if !filepath.IsAbs(path) {
			if abs, err := filepath.Abs(path); err == nil {
				absPath = abs
			}
		}
		info, err := os.Stat(absPath)
		if err != nil {
			skipped = append(skipped, path+" (not found)")
			continue
		}
		if info.IsDir() {
			skipped = append(skipped, path+" (directory)")
			continue
		}
		if info.Size() > maxAttachedFileBytes {
			skipped = append(skipped, fmt.Sprintf("%s (>%dKB)", path, maxAttachedFileBytes/1024))
			continue
		}
		data, err := os.ReadFile(absPath) //nolint:gosec
		if err != nil {
			skipped = append(skipped, path+" (read error)")
			continue
		}
		attachedPaths = append(attachedPaths, path)
		fmt.Fprintf(&attachments, "\n<attached_file path=%q>\n%s\n</attached_file>", path, string(data))
	}
	if attachments.Len() == 0 {
		return text, attachedPaths, skipped
	}
	return text + attachments.String(), attachedPaths, skipped
}
