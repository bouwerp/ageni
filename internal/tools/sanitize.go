package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Path          string `json:"path"`
	Path2         string `json:"Path"`
	TargetFile    string `json:"TargetFile"`
	TargetFile2   string `json:"target_file"`
	File          string `json:"file"`
	File2         string `json:"File"`
	Filepath      string `json:"filepath"`
	Filepath2     string `json:"filePath"`
	Filepath3     string `json:"FilePath"`
	Filename      string `json:"filename"`
	Filename2     string `json:"fileName"`
	Filename3     string `json:"FileName"`
	AbsolutePath  string `json:"absolute_path"`
	AbsolutePath2 string `json:"AbsolutePath"`
	AbsolutePath3 string `json:"absolutePath"`
	AbsPath       string `json:"abs_path"`
}

func ResolvePath(args json.RawMessage) string {
	var p pathArgs
	if err := json.Unmarshal(args, &p); err == nil {
		var resolved string
		if p.Path != "" {
			resolved = p.Path
		} else if p.Path2 != "" {
			resolved = p.Path2
		} else if p.TargetFile != "" {
			resolved = p.TargetFile
		} else if p.TargetFile2 != "" {
			resolved = p.TargetFile2
		} else if p.File != "" {
			resolved = p.File
		} else if p.File2 != "" {
			resolved = p.File2
		} else if p.Filepath != "" {
			resolved = p.Filepath
		} else if p.Filepath2 != "" {
			resolved = p.Filepath2
		} else if p.Filepath3 != "" {
			resolved = p.Filepath3
		} else if p.Filename != "" {
			resolved = p.Filename
		} else if p.Filename2 != "" {
			resolved = p.Filename2
		} else if p.Filename3 != "" {
			resolved = p.Filename3
		} else if p.AbsolutePath != "" {
			resolved = p.AbsolutePath
		} else if p.AbsolutePath2 != "" {
			resolved = p.AbsolutePath2
		} else if p.AbsolutePath3 != "" {
			resolved = p.AbsolutePath3
		} else if p.AbsPath != "" {
			resolved = p.AbsPath
		}
		if resolved != "" {
			return CleanAndMapPath(resolved)
		}
	}
	return ""
}

type queryArgs struct {
	Query       string `json:"query"`
	Query2      string `json:"Query"`
	Pattern     string `json:"pattern"`
	Pattern2    string `json:"Pattern"`
	Regex       string `json:"regex"`
	Regex2      string `json:"Regex"`
	Glob        string `json:"glob"`
	Glob2       string `json:"Glob"`
	Search      string `json:"search"`
	Search2     string `json:"Search"`
	Q           string `json:"q"`
	Q2          string `json:"Q"`
	GlobPattern string `json:"glob_pattern"`
}

func ResolveQuery(args json.RawMessage) string {
	var p queryArgs
	if err := json.Unmarshal(args, &p); err == nil {
		if p.Query != "" {
			return p.Query
		}
		if p.Query2 != "" {
			return p.Query2
		}
		if p.Pattern != "" {
			return p.Pattern
		}
		if p.Pattern2 != "" {
			return p.Pattern2
		}
		if p.Regex != "" {
			return p.Regex
		}
		if p.Regex2 != "" {
			return p.Regex2
		}
		if p.Glob != "" {
			return p.Glob
		}
		if p.Glob2 != "" {
			return p.Glob2
		}
		if p.Search != "" {
			return p.Search
		}
		if p.Search2 != "" {
			return p.Search2
		}
		if p.Q != "" {
			return p.Q
		}
		if p.Q2 != "" {
			return p.Q2
		}
		if p.GlobPattern != "" {
			return p.GlobPattern
		}
	}
	return ""
}

// ValidatePath resolves the absolute path of the input and checks if it escapes the current working directory.
// It returns the absolute path if it's within the current working directory, or if it is a legitimate system/temp path.
func ValidatePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	path = CleanAndMapPath(path)
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %w", err)
	}
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		cwdAbs = filepath.Clean(cwd)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = filepath.Clean(path)
	}

	// 1. If it's inside the workspace, it is allowed.
	rel, err := filepath.Rel(cwdAbs, absPath)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return absPath, nil
	}

	// 2. If it's inside the OS temp directory, it is allowed.
	tmpDir := os.TempDir()
	tmpAbs, err := filepath.Abs(tmpDir)
	if err == nil {
		relTmp, err := filepath.Rel(tmpAbs, absPath)
		if err == nil && relTmp != ".." && !strings.HasPrefix(relTmp, ".."+string(filepath.Separator)) {
			return absPath, nil
		}
	}

	// Also check explicit "/tmp" prefix for Unix environments
	if strings.HasPrefix(absPath, "/tmp/") || absPath == "/tmp" {
		return absPath, nil
	}

	// 3. If it's a legitimate system/library path, it is allowed.
	// Standard library search and system configuration prefixes:
	allowedPrefixes := []string{
		"/usr/", "/lib/", "/lib64/", "/etc/", "/opt/",
		"/System/", "/Library/", // macOS support
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(absPath, prefix) {
			return absPath, nil
		}
	}

	// Otherwise, it escapes and is disallowed.
	return "", fmt.Errorf("path %q escapes the workspace root %q. Venturing outside the current working directory is disallowed unless explicitly required", path, cwdAbs)
}

// CleanAndMapPath maps paths from other/alternate repository directories
// to the current workspace root directory.
func CleanAndMapPath(path string) string {
	if path == "" {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		cwdAbs = filepath.Clean(cwd)
	}

	// Clean the input path
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = filepath.Clean(path)
	}

	// If it already is inside the cwd, no mapping needed.
	rel, err := filepath.Rel(cwdAbs, absPath)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return absPath
	}

	// Check if the path is in the same parent directory as CWD.
	// E.g. CWD is `/home/code/repos/repoA`, and path is `/home/code/repos/repoB/src/main.go`.
	// We want to map it to `/home/code/repos/repoA/src/main.go`.
	parentDir := filepath.Dir(cwdAbs)
	if parentDir != "" && parentDir != "/" && parentDir != "." {
		prefix := parentDir + string(filepath.Separator)
		if strings.HasPrefix(absPath, prefix) {
			sub := strings.TrimPrefix(absPath, prefix)
			// Find the first separator separating the other repo name from the rest of the path
			slashIdx := strings.IndexByte(sub, filepath.Separator)
			if slashIdx >= 0 {
				relPath := sub[slashIdx+1:]
				mapped := filepath.Join(cwdAbs, relPath)
				return mapped
			}
		}
	}
	return absPath
}

// ResolveContent best-effort extracts content from raw tool arguments.
// It supports case-insensitive variations of content/text keys, and also
// reconstructs search/replace or old/new string pairs into Aider blocks.
func ResolveContent(args json.RawMessage) string {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(args, &rawMap); err != nil {
		return ""
	}

	// Lowercase all keys to make lookup case-insensitive
	m := make(map[string]string)
	for k, v := range rawMap {
		var strVal string
		if err := json.Unmarshal(v, &strVal); err == nil {
			m[strings.ToLower(k)] = strVal
		}
	}

	// 1. Direct content fields
	contentKeys := []string{
		"content", "text", "code", "body", "value",
		"new_content", "newcontent",
	}
	for _, k := range contentKeys {
		if val, ok := m[k]; ok && val != "" {
			return val
		}
	}

	// 2. Search / Replace block fields
	var searchVal, replaceVal string
	searchKeys := []string{"search", "search_block", "searchblock"}
	replaceKeys := []string{"replace", "replace_block", "replaceblock"}
	for _, k := range searchKeys {
		if val, ok := m[k]; ok {
			searchVal = val
			break
		}
	}
	for _, k := range replaceKeys {
		if val, ok := m[k]; ok {
			replaceVal = val
			break
		}
	}
	if searchVal != "" || replaceVal != "" {
		return fmt.Sprintf("<<<<<<< SEARCH\n%s\n=======\n%s\n>>>>>>> REPLACE\n", searchVal, replaceVal)
	}

	// 3. Old / New string fields
	var oldVal, newVal string
	oldKeys := []string{"old_string", "oldstring", "old", "find"}
	newKeys := []string{"new_string", "newstring", "new", "replacement"}
	for _, k := range oldKeys {
		if val, ok := m[k]; ok {
			oldVal = val
			break
		}
	}
	for _, k := range newKeys {
		if val, ok := m[k]; ok {
			newVal = val
			break
		}
	}
	if oldVal != "" || newVal != "" {
		return fmt.Sprintf("<<<<<<< SEARCH\n%s\n=======\n%s\n>>>>>>> REPLACE\n", oldVal, newVal)
	}

	return ""
}

// ResolveOldNewStrings best-effort extracts old_string and new_string from raw tool arguments.
func ResolveOldNewStrings(args json.RawMessage) (string, string, bool) {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(args, &rawMap); err != nil {
		return "", "", false
	}

	// Lowercase all keys to make lookup case-insensitive
	m := make(map[string]string)
	for k, v := range rawMap {
		var strVal string
		if err := json.Unmarshal(v, &strVal); err == nil {
			m[strings.ToLower(k)] = strVal
		}
	}

	var oldVal, newVal string
	var foundOld, foundNew bool
	oldKeys := []string{"old_string", "oldstring", "old", "find"}
	newKeys := []string{"new_string", "newstring", "new", "replacement"}
	for _, k := range oldKeys {
		if val, ok := m[k]; ok {
			oldVal = val
			foundOld = true
			break
		}
	}
	for _, k := range newKeys {
		if val, ok := m[k]; ok {
			newVal = val
			foundNew = true
			break
		}
	}
	return oldVal, newVal, foundOld && foundNew
}



