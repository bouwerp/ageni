package tools

import (
	"regexp"
	"strings"
)

// ansiRE matches ANSI/VT100 escape sequences: CSI sequences (ESC [ ... letter)
// and two-character sequences (ESC + single non-[ char).
var ansiRE = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[A-Za-z]|[^\[])`)

// sanitizeOutput strips ANSI escape sequences and bare control characters
// (0x00–0x1F, 0x7F) from tool output, preserving \n, \r, \t. This prevents
// raw ESC bytes from ending up in JSON string values, which would cause 400
// "Invalid control character" errors from strict JSON parsers (e.g. OpenRouter).
func sanitizeOutput(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	// Strip any remaining lone ESC or other control chars not caught by regex.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 0x20 || r == '\n' || r == '\r' || r == '\t' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
