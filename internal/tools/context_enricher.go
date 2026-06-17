package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var errorContextRegex = regexp.MustCompile(`(?m)^([a-zA-Z0-9_\-\.\/]+\.(?:go|py|js|ts|rs)):(\d+)(?::\d+)?:?\s+(.*)$`)

// EnrichErrorContext parses the output for file/line error references and
// injects a few lines of the target file's content directly beneath the error
// message to save the LLM a round-trip view_file call.
func EnrichErrorContext(out string) string {
	matches := errorContextRegex.FindAllStringSubmatch(out, 10)
	if len(matches) == 0 {
		return out
	}

	enrichedCount := 0
	seen := make(map[string]bool)

	lines := strings.Split(out, "\n")
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
		
		if enrichedCount >= 3 {
			continue
		}

		m := errorContextRegex.FindStringSubmatch(line)
		if m != nil {
			file := m[1]
			lineNumStr := m[2]
			lineNum, err := strconv.Atoi(lineNumStr)
			if err != nil {
				continue
			}
			
			key := fmt.Sprintf("%s:%d", file, lineNum)
			if seen[key] {
				continue
			}
			seen[key] = true
			
			content, err := os.ReadFile(file)
			if err == nil {
				fileLines := strings.Split(string(content), "\n")
				start := lineNum - 3
				if start < 0 { start = 0 }
				end := lineNum + 2
				if end > len(fileLines) { end = len(fileLines) }
				
				sb.WriteString(fmt.Sprintf("    |-- Context (%s:%d-%d) --\n", filepath.Base(file), start+1, end))
				for i := start; i < end; i++ {
					prefix := "    | "
					if i == lineNum-1 {
						prefix = "    | > "
					}
					sb.WriteString(fmt.Sprintf("%s%d: %s\n", prefix, i+1, fileLines[i]))
				}
				sb.WriteString("    |---------------------------\n")
				enrichedCount++
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
