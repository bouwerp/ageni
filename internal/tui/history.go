package tui

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bouwerp/ageni/internal/homedir"
)

// History is a persistent ring of user-entered messages. It survives across
// ageni sessions via ~/.ageni/history.txt. Capped at historyMax entries on
// disk; older entries are dropped when the cap is reached.
const historyMax = 1000

type History struct {
	mu    sync.Mutex
	items []string
	path  string
}

// LoadHistory reads ~/.ageni/history.txt (one entry per line, possibly
// multi-line entries delimited by an explicit "\\n" escape). Missing file
// is not an error.
func LoadHistory() *History {
	h := &History{}
	if home, err := homedir.Dir(); err == nil {
		h.path = filepath.Join(home, ".ageni", "history.txt")
	}
	h.load()
	return h
}

func (h *History) load() {
	if h.path == "" {
		return
	}
	f, err := os.Open(h.path) //nolint:gosec
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		h.items = append(h.items, decodeEntry(line))
	}
	if len(h.items) > historyMax {
		h.items = h.items[len(h.items)-historyMax:]
	}
}

// Append records a new entry and persists it.
func (h *History) Append(entry string) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.items); n > 0 && h.items[n-1] == entry {
		return // ignore duplicate of most recent
	}
	h.items = append(h.items, entry)
	if len(h.items) > historyMax {
		h.items = h.items[len(h.items)-historyMax:]
	}
	h.persist(entry)
}

func (h *History) persist(entry string) {
	if h.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil { //nolint:gosec
		return
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(encodeEntry(entry) + "\n")

	// Periodically truncate so the file doesn't grow forever.
	if len(h.items)%historyMax == 0 {
		h.rewrite()
	}
}

func (h *History) rewrite() {
	if h.path == "" {
		return
	}
	tmp := h.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	for _, e := range h.items {
		_, _ = w.WriteString(encodeEntry(e) + "\n")
	}
	_ = w.Flush()
	_ = f.Close()
	_ = os.Rename(tmp, h.path)
}

// Items returns a copy of the entries oldest-first.
func (h *History) Items() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.items))
	copy(out, h.items)
	return out
}

// encodeEntry collapses newlines into "\\n" so each entry occupies one line
// in the file. Survives round-tripping through decodeEntry.
func encodeEntry(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

func decodeEntry(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				sb.WriteByte('\n')
				i++
				continue
			case '\\':
				sb.WriteByte('\\')
				i++
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}
