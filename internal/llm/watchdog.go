package llm

import (
	"fmt"
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
