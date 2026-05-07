package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ansiRE matches ANSI/VT100 escape sequences in plain text.
var ansiRE = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[A-Za-z]|[^\[])`)

// SanitizeText strips ANSI escape sequences and bare control characters
// (0x00–0x1F, 0x7F) from plain-text strings, preserving \n, \r, \t.
// Applied to Message.Text and ToolResult.Content before building API params
// so providers that reject even properly-escaped control characters don't 400.
func SanitizeText(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 0x20 || r == '\n' || r == '\r' || r == '\t' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sanitizeArgs ensures tool call argument bytes are valid JSON. LLMs
// occasionally emit raw control characters (0x00-0x1F) inside JSON string
// literals — most commonly a literal newline ('\n' = 0x0a) — which is
// rejected by providers with "invalid character in string literal". It also
// handles truncated responses by attempting to close unterminated strings
// and JSON blocks.
//
// Fast path: json.Valid passes → return as-is.
// Slow path: scan byte-by-byte inside string literals and replace bare
// control characters with their proper JSON escape sequences, then heal
// any unterminated structures.
func sanitizeArgs(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	if json.Valid(raw) {
		return json.RawMessage(raw)
	}
	s := escapeControlsInJSON(string(raw))
	s = healJSON(s)
	return json.RawMessage(s)
}

// healJSON attempts to close any unterminated string literals, objects, or
// arrays in a truncated JSON string.
func healJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}

	inStr := false
	escaped := false
	var stack []rune

	for i := 0; i < len(s); i++ {
		r := rune(s[i])
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inStr {
			escaped = true
			continue
		}
		if r == '"' {
			inStr = !inStr
			continue
		}
		if !inStr {
			if r == '{' || r == '[' {
				stack = append(stack, r)
			} else if r == '}' || r == ']' {
				if len(stack) > 0 {
					// Pop regardless of match — we're healing, not validating.
					stack = stack[:len(stack)-1]
				}
			}
		}
	}

	if escaped {
		// String ended with a backslash; remove it so it doesn't escape our
		// added quote or leave the JSON malformed.
		s = s[:len(s)-1]
	}
	if inStr {
		s += `"`
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			s += "}"
		} else {
			s += "]"
		}
	}
	return s
}

// escapeControlsInJSON walks a potentially-invalid JSON string and escapes
// any bare control characters found inside string literals. Characters
// outside string literals are passed through unchanged.
func escapeControlsInJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' && inStr {
			b.WriteByte(c)
			escaped = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			b.WriteByte(c)
			continue
		}
		if inStr && c < 0x20 {
			switch c {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
