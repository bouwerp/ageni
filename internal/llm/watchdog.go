package llm

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// StreamIdleTimeout is the maximum time the master stream loop will wait
// between consecutive events before declaring the model unresponsive.
// This covers both "time to first token" hangs and mid-stream stalls caused
// by a silent TCP connection.
const StreamIdleTimeout = 2 * time.Minute

// ErrStreamIdle is returned (wrapped in StreamEventError) when no event
// arrives within StreamIdleTimeout. Callers can use IsStreamIdle to detect it.
var ErrStreamIdle = fmt.Errorf("model not responding: no data received for %s", StreamIdleTimeout)

// IsStreamIdle reports whether an error originated from the idle watchdog.
func IsStreamIdle(err error) bool {
	if err == nil {
		return false
	}
	return err == ErrStreamIdle
}

// IsTransientStreamError reports whether err is a transient network/protocol
// error that is safe to retry (e.g. "unexpected end of JSON input", EOF,
// connection reset by peer, broken pipe). These are distinct from semantic API
// errors (4xx) which should not be retried.
func IsTransientStreamError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return true
	}
	s := err.Error()
	for _, substr := range []string{
		"unexpected end of JSON",
		"EOF",
		"connection reset by peer",
		"broken pipe",
		"connection refused",
		"i/o timeout",
		"read: connection",
		"write: connection",
		"transport connection broken",
		"server closed idle connection",
	} {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// WatchdogStream wraps src with an idle timer. If no event arrives within
// StreamIdleTimeout, a StreamEventError{ErrStreamIdle} is emitted, the source
// is drained in the background, and the returned channel is closed.
//
// Normal completion (StreamEventDone or source close) cancels the timer and
// passes all events through unchanged.
func WatchdogStream(src <-chan StreamEvent) <-chan StreamEvent {
	out := make(chan StreamEvent, 8)
	go func() {
		defer close(out)
		t := time.NewTimer(StreamIdleTimeout)
		defer t.Stop()
		for {
			select {
			case ev, ok := <-src:
				if !ok {
					return
				}
				// Reset the idle timer on every incoming event.
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
				t.Reset(StreamIdleTimeout)
				out <- ev
				// After an error or done, drain the rest and exit.
				if ev.Type == StreamEventError || ev.Type == StreamEventDone {
					go func() {
						for range src { //nolint:revive
						}
					}()
					return
				}
			case <-t.C:
				out <- StreamEvent{Type: StreamEventError, Err: ErrStreamIdle}
				// Drain the abandoned source so its goroutine can exit.
				go func() {
					for range src { //nolint:revive
					}
				}()
				return
			}
		}
	}()
	return out
}
