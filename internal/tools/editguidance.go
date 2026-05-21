package tools

import "fmt"

func suggestApplyDiffForNoMatch(path string) string {
	return fmt.Sprintf("old_string not found in %s; if this is a multi-line or approximate edit, prefer apply_diff with SEARCH/REPLACE blocks because it returns closest candidate regions when SEARCH misses", path)
}

func suggestApplyDiffForMultipleMatches(count int, path string) string {
	return fmt.Sprintf("old_string occurs %d times in %s; provide more context to make it unique, set replace_all where supported, or prefer apply_diff for multi-block edits", count, path)
}
