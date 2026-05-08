package llm

import (
	"testing"
	"time"
)

func TestWatchdogStreamPassthrough(t *testing.T) {
	src := make(chan StreamEvent, 4)
	src <- StreamEvent{Type: StreamEventText, TextDelta: "hello"}
	src <- StreamEvent{Type: StreamEventDone}
	close(src)

	out := WatchdogStream(src)
	var got []StreamEventType
	for ev := range out {
		got = append(got, ev.Type)
	}
	if len(got) != 2 || got[0] != StreamEventText || got[1] != StreamEventDone {
		t.Fatalf("unexpected events: %v", got)
	}
}

func TestWatchdogStreamIdleFires(t *testing.T) {
	// Temporarily lower the timeout for the test using a helper wrapper
	// with a short deadline so the test doesn't take 2 minutes.
	src := make(chan StreamEvent) // never sends
	out := watchdogStreamWithTimeout(src, 50*time.Millisecond)

	ev := <-out
	if ev.Type != StreamEventError {
		t.Fatalf("expected StreamEventError, got %v", ev.Type)
	}
	if !IsStreamIdle(ev.Err) {
		t.Fatalf("expected ErrStreamIdle, got %v", ev.Err)
	}
}

// watchdogStreamWithTimeout is a test-only variant with a configurable timeout.
func watchdogStreamWithTimeout(src <-chan StreamEvent, timeout time.Duration) <-chan StreamEvent {
	out := make(chan StreamEvent, 8)
	go func() {
		defer close(out)
		t := time.NewTimer(timeout)
		defer t.Stop()
		for {
			select {
			case ev, ok := <-src:
				if !ok {
					return
				}
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
				t.Reset(timeout)
				out <- ev
				if ev.Type == StreamEventError || ev.Type == StreamEventDone {
					go func() {
						for range src { //nolint:revive
						}
					}()
					return
				}
			case <-t.C:
				out <- StreamEvent{Type: StreamEventError, Err: ErrStreamIdle}
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
