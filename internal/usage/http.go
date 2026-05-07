package usage

import "io"

// bodySnippet reads up to maxBytes from rc and returns it as a string.
func bodySnippet(rc io.ReadCloser, maxBytes int64) string {
	defer rc.Close()
	b, _ := io.ReadAll(io.LimitReader(rc, maxBytes))
	return string(b)
}
