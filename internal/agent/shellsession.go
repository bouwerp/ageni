package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ShellStatus represents the state of a shell session.
type ShellStatus int

const (
	ShellStatusOpen   ShellStatus = iota
	ShellStatusExited             // process exited on its own
	ShellStatusClosed             // closed by Close()
)

// ShellKind distinguishes long-running service shells from short-lived task shells.
type ShellKind string

const (
	// ShellKindTask is a short-lived shell for one-off commands (default).
	ShellKindTask ShellKind = "task"
	// ShellKindService is a long-running shell for servers/daemons (e.g. a dev
	// server). It is displayed more prominently in the TUI and an unexpected
	// exit is treated as a warning rather than normal completion.
	ShellKindService ShellKind = "service"
)

const (
	ringBufSize   = 512 * 1024 // 512 KB
	shellSentinel = "__AGENI_SHELL_DONE__:"
)

// ShellSession wraps a persistent bash process with a ring buffer for output.
type ShellSession struct {
	id    string
	label string    // human-readable name, e.g. "Metro Server"
	kind  ShellKind // "task" or "service"
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte // ring buffer data (allocated once to ringBufSize)
	head   int    // write position in ring buffer
	total  int64  // total bytes ever written (global offset)
	status ShellStatus

	bus *Bus
}

func newShellSession(id, label string, kind ShellKind, bus *Bus) (*ShellSession, error) {
	cmd := exec.Command("bash", "-s")
	// Put the shell in its own process group so that when we kill it we also
	// kill any child processes it has spawned (servers, build tools, etc.).
	shellSetPgid(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	s := &ShellSession{
		id:    id,
		label: label,
		kind:  kind,
		cmd:   cmd,
		stdin: stdin,
		buf:   make([]byte, ringBufSize),
		bus:   bus,
	}
	s.cond = sync.NewCond(&s.mu)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start bash: %w", err)
	}

	// Reader goroutine: copies from pipe to ring buffer
	go func() {
		chunk := make([]byte, 4096)
		for {
			n, err := pr.Read(chunk)
			if n > 0 {
				s.write(chunk[:n])
			}
			if err != nil {
				break
			}
		}
	}()

	// Wait goroutine: waits for process exit, then closes pipe
	go func() {
		_ = cmd.Wait()
		pw.Close()
		s.mu.Lock()
		if s.status == ShellStatusOpen {
			s.status = ShellStatusExited
		}
		s.cond.Broadcast()
		s.mu.Unlock()
		if bus != nil {
			bus.Publish(Event{Kind: EvShellExited, SubagentID: id})
		}
	}()

	return s, nil
}

func (s *ShellSession) write(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, b := range data {
		s.buf[s.head] = b
		s.head = (s.head + 1) % ringBufSize
		s.total++
	}
	s.cond.Broadcast()

	if s.bus != nil {
		s.bus.Publish(Event{Kind: EvShellOutput, SubagentID: s.id, Text: string(data)})
	}
}

// ID returns the session ID.
func (s *ShellSession) ID() string { return s.id }

// Label returns the human-readable name for this session.
func (s *ShellSession) Label() string { return s.label }

// Kind returns the shell kind (task or service).
func (s *ShellSession) Kind() ShellKind { return s.kind }

// Status returns the current shell status.
func (s *ShellSession) Status() ShellStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// TotalBytes returns the total number of bytes written to the ring buffer.
func (s *ShellSession) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// ReadFrom reads up to maxBytes starting at globalOffset. Returns the data and
// the next global offset. If globalOffset is before the buffer's base, reading
// starts from the oldest available byte.
func (s *ShellSession) ReadFrom(globalOffset int64, maxBytes int) ([]byte, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	base := s.total - int64(min(int(s.total), ringBufSize))
	if globalOffset < base {
		globalOffset = base
	}
	if globalOffset >= s.total {
		return nil, s.total
	}

	available := int(s.total - globalOffset)
	if maxBytes > 0 && available > maxBytes {
		available = maxBytes
	}

	out := make([]byte, available)
	// position in ring buffer for globalOffset
	ringPos := int(globalOffset % int64(ringBufSize))
	for i := 0; i < available; i++ {
		out[i] = s.buf[(ringPos+i)%ringBufSize]
	}
	return out, globalOffset + int64(available)
}

// TailLines returns the last n lines from the buffer.
func (s *ShellSession) TailLines(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	size := int(min(int(s.total), ringBufSize))
	if size == 0 {
		return ""
	}

	// Build a linear copy
	data := make([]byte, size)
	start := int(s.total % int64(ringBufSize))
	if int(s.total) < ringBufSize {
		// not yet wrapped
		copy(data, s.buf[:size])
	} else {
		copy(data, s.buf[start:])
		copy(data[ringBufSize-start:], s.buf[:start])
	}

	lines := bytes.Split(data, []byte("\n"))
	// Remove trailing empty
	for len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if n > len(lines) {
		n = len(lines)
	}
	result := lines[len(lines)-n:]
	parts := make([]string, len(result))
	for i, l := range result {
		parts[i] = string(l)
	}
	return strings.Join(parts, "\n")
}

// SendInput writes raw bytes to the shell's stdin.
func (s *ShellSession) SendInput(input string) error {
	s.mu.Lock()
	st := s.status
	s.mu.Unlock()
	if st != ShellStatusOpen {
		return fmt.Errorf("shell %s is not open (status=%d)", s.id, st)
	}
	_, err := fmt.Fprint(s.stdin, input)
	return err
}

// WaitForPattern blocks until pattern appears in buf at or after startOffset,
// or until ctx/timeout expires. Returns the global offset just after the match.
func (s *ShellSession) WaitForPattern(ctx context.Context, pattern string, startOffset int64, timeout time.Duration) (int64, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		case <-done:
		}
	}()
	defer close(done)

	s.mu.Lock()
	defer s.mu.Unlock()

	searchFrom := startOffset
	for {
		// Check if context is done
		select {
		case <-ctx.Done():
			return searchFrom, ctx.Err()
		default:
		}

		// Read from searchFrom to total
		base := s.total - int64(min(int(s.total), ringBufSize))
		if searchFrom < base {
			searchFrom = base
		}

		if searchFrom < s.total {
			size := int(s.total - searchFrom)
			data := make([]byte, size)
			ringPos := int(searchFrom % int64(ringBufSize))
			for i := 0; i < size; i++ {
				data[i] = s.buf[(ringPos+i)%ringBufSize]
			}
			idx := bytes.Index(data, []byte(pattern))
			if idx >= 0 {
				return searchFrom + int64(idx) + int64(len(pattern)), nil
			}
			// advance searchFrom to avoid re-scanning, keep overlap for pattern boundary
			overlap := len(pattern) - 1
			if overlap < 0 {
				overlap = 0
			}
			advance := size - overlap
			if advance > 0 {
				searchFrom += int64(advance)
			}
		}

		if s.status != ShellStatusOpen {
			return searchFrom, fmt.Errorf("shell exited before pattern found")
		}

		s.cond.Wait()
	}
}

// Exec executes a command in the shell.
// If waitDone is true (sync mode): appends sentinel, waits for it, strips it from output.
// If waitDone is false (async mode): just writes the command and returns immediately.
func (s *ShellSession) Exec(ctx context.Context, command string, timeout time.Duration, waitDone bool) (string, error) {
	s.mu.Lock()
	st := s.status
	s.mu.Unlock()
	if st != ShellStatusOpen {
		return "", fmt.Errorf("shell %s is not open", s.id)
	}

	if !waitDone {
		if _, err := fmt.Fprintf(s.stdin, "%s\n", command); err != nil {
			return "", fmt.Errorf("write command: %w", err)
		}
		return "queued (async)", nil
	}

	// Sync mode: record start offset, write command + sentinel
	s.mu.Lock()
	startOffset := s.total
	s.mu.Unlock()

	if _, err := fmt.Fprintf(s.stdin, "%s\necho '%s'\"$?\"\n", command, shellSentinel); err != nil {
		return "", fmt.Errorf("write command: %w", err)
	}

	// Wait for sentinel in output
	sentinelOffset, err := s.WaitForPattern(ctx, shellSentinel, startOffset, timeout)
	if err != nil {
		return "", fmt.Errorf("waiting for sentinel: %w", err)
	}

	// Read all output from startOffset to sentinelOffset
	s.mu.Lock()
	data, _ := s.readRangeUnlocked(startOffset, int(sentinelOffset-startOffset))
	s.mu.Unlock()

	// Find and parse the sentinel line
	output := string(data)
	sentinelIdx := strings.Index(output, shellSentinel)
	var exitCode int
	var cleanOutput string
	if sentinelIdx >= 0 {
		// Extract exit code from sentinel line
		rest := output[sentinelIdx+len(shellSentinel):]
		// rest looks like "0\n" or "1\n..."
		lineEnd := strings.IndexByte(rest, '\n')
		exitCodeStr := rest
		if lineEnd >= 0 {
			exitCodeStr = rest[:lineEnd]
		}
		fmt.Sscanf(exitCodeStr, "%d", &exitCode)
		// Clean output: everything before sentinel
		cleanOutput = strings.TrimRight(output[:sentinelIdx], "\n")
	} else {
		cleanOutput = strings.TrimRight(output, "\n")
	}

	if exitCode != 0 {
		return cleanOutput + fmt.Sprintf("\n[exit %d]", exitCode), nil
	}
	return cleanOutput, nil
}

// readRangeUnlocked reads size bytes starting at globalOffset. Must be called with mu held.
func (s *ShellSession) readRangeUnlocked(globalOffset int64, size int) ([]byte, int64) {
	base := s.total - int64(min(int(s.total), ringBufSize))
	if globalOffset < base {
		globalOffset = base
	}
	available := int(s.total - globalOffset)
	if size > available {
		size = available
	}
	if size <= 0 {
		return nil, globalOffset
	}
	out := make([]byte, size)
	ringPos := int(globalOffset % int64(ringBufSize))
	for i := 0; i < size; i++ {
		out[i] = s.buf[(ringPos+i)%ringBufSize]
	}
	return out, globalOffset + int64(size)
}

func (s *ShellSession) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != ShellStatusOpen {
		return nil
	}
	s.status = ShellStatusClosed
	s.cond.Broadcast()
	s.stdin.Close()
	if s.cmd.Process != nil {
		// Kill the entire process group so child processes (servers, build
		// tools, etc.) don't linger after the shell exits.
		shellKillGroup(s.cmd.Process)
	}
	return nil
}

// ShellManager manages multiple shell sessions.
type ShellManager struct {
	mu      sync.Mutex
	shells  map[string]*ShellSession
	nextID  int
	bus     *Bus
	cancelF context.CancelFunc
	ctx     context.Context
}

// NewShellManager creates a new ShellManager.
func NewShellManager(bus *Bus) *ShellManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &ShellManager{
		shells:  make(map[string]*ShellSession),
		bus:     bus,
		ctx:     ctx,
		cancelF: cancel,
	}
}

// Open starts a new bash session and returns it.
// label is a human-readable name (empty string is fine for tasks).
// kind is ShellKindTask (default) or ShellKindService for long-running servers.
func (m *ShellManager) Open(label string, kind ShellKind) (*ShellSession, error) {
	if kind == "" {
		kind = ShellKindTask
	}
	m.mu.Lock()
	m.nextID++
	id := fmt.Sprintf("sh%d", m.nextID)
	m.mu.Unlock()

	s, err := newShellSession(id, label, kind, m.bus)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.shells[id] = s
	m.mu.Unlock()

	if m.bus != nil {
		m.bus.Publish(Event{Kind: EvShellOpened, SubagentID: id, Text: label, ShellKind: kind})
	}
	return s, nil
}

// Get returns the session with the given ID.
func (m *ShellManager) Get(id string) (*ShellSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.shells[id]
	return s, ok
}

// List returns all sessions.
func (m *ShellManager) List() []*ShellSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ShellSession, 0, len(m.shells))
	for _, s := range m.shells {
		out = append(out, s)
	}
	return out
}

// Close closes the session with the given ID.
func (m *ShellManager) Close(id string) error {
	m.mu.Lock()
	s, ok := m.shells[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such shell: %s", id)
	}
	return s.close()
}

// CancelAll closes all open sessions.
func (m *ShellManager) CancelAll() {
	m.mu.Lock()
	shells := make([]*ShellSession, 0, len(m.shells))
	for _, s := range m.shells {
		shells = append(shells, s)
	}
	m.mu.Unlock()
	for _, s := range shells {
		_ = s.close()
	}
	m.cancelF()
}
