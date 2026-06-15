package tools

import (
	"encoding/json"
	"regexp"
	"strconv"
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

func collapseBlankLines(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	var out []string
	blankRun := 0
	collapsed := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun <= 2 {
				out = append(out, "")
			} else {
				collapsed++
			}
			continue
		}
		blankRun = 0
		out = append(out, line)
	}
	result := strings.Join(out, "\n")
	if collapsed > 0 {
		result = strings.TrimRight(result, "\n") + "\n[collapsed " + strconv.Itoa(collapsed) + " blank line(s)]"
	}
	return result
}

type pathArgs struct {
	Path         string `json:"path"`
	TargetFile   string `json:"TargetFile"`
	TargetFile2  string `json:"target_file"`
	File         string `json:"file"`
	Filepath     string `json:"filepath"`
	Filepath2    string `json:"filePath"`
	Filename     string `json:"filename"`
	Filename2    string `json:"fileName"`
	AbsolutePath string `json:"absolute_path"`
	AbsPath      string `json:"abs_path"`
}

func resolvePath(args json.RawMessage) string {
	var p pathArgs
	if err := json.Unmarshal(args, &p); err == nil {
		if p.Path != "" {
			return p.Path
		}
		if p.TargetFile != "" {
			return p.TargetFile
		}
		if p.TargetFile2 != "" {
			return p.TargetFile2
		}
		if p.File != "" {
			return p.File
		}
		if p.Filepath != "" {
			return p.Filepath
		}
		if p.Filepath2 != "" {
			return p.Filepath2
		}
		if p.Filename != "" {
			return p.Filename
		}
		if p.Filename2 != "" {
			return p.Filename2
		}
		if p.AbsolutePath != "" {
			return p.AbsolutePath
		}
		if p.AbsPath != "" {
			return p.AbsPath
		}
	}
	return ""
}
