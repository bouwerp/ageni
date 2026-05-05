package llm

import (
	"context"
	"errors"
	"testing"
)

type stubAdapter struct {
	name      string
	syncErr   error             // returned synchronously from Stream
	events    []StreamEvent     // emitted on the channel after Stream returns
	gotModel  string            // captures the Model field of the last request
	streamErr error             // emitted as a StreamEventError BEFORE events
}

func (s *stubAdapter) Provider() string { return s.name }
func (s *stubAdapter) Stream(_ context.Context, req Request) (<-chan StreamEvent, error) {
	s.gotModel = req.Model
	if s.syncErr != nil {
		return nil, s.syncErr
	}
	ch := make(chan StreamEvent, 4+len(s.events))
	if s.streamErr != nil {
		ch <- StreamEvent{Type: StreamEventError, Err: s.streamErr}
		close(ch)
		return ch, nil
	}
	for _, ev := range s.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func TestFallbackOn429Sync(t *testing.T) {
	primary := &stubAdapter{name: "p", syncErr: errors.New("HTTP 429: rate limit")}
	secondary := &stubAdapter{name: "s", events: []StreamEvent{
		{Type: StreamEventText, TextDelta: "hello"},
		{Type: StreamEventDone},
	}}
	chain := NewFallbackAdapter("test",
		FallbackEntry{Adapter: primary, Model: "p-model", Label: "p/p-model"},
		FallbackEntry{Adapter: secondary, Model: "s-model", Label: "s/s-model"},
	)
	got := []string{}
	chain.OnFallback = func(from, to, reason string) { got = append(got, from+"->"+to) }
	ch, err := chain.Stream(context.Background(), Request{Model: "ignored"})
	if err != nil {
		t.Fatalf("unexpected sync err: %v", err)
	}
	var text string
	for ev := range ch {
		if ev.Type == StreamEventText {
			text += ev.TextDelta
		}
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
	if secondary.gotModel != "s-model" {
		t.Fatalf("model rewrite failed: got %q", secondary.gotModel)
	}
	if len(got) != 1 || got[0] != "p/p-model->s/s-model" {
		t.Fatalf("OnFallback: %v", got)
	}
}

func TestFallbackOnStreamError(t *testing.T) {
	primary := &stubAdapter{name: "p", streamErr: errors.New("503 service unavailable")}
	secondary := &stubAdapter{name: "s", events: []StreamEvent{
		{Type: StreamEventText, TextDelta: "ok"},
		{Type: StreamEventDone},
	}}
	chain := NewFallbackAdapter("test",
		FallbackEntry{Adapter: primary, Model: "p", Label: "p"},
		FallbackEntry{Adapter: secondary, Model: "s", Label: "s"},
	)
	ch, err := chain.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for ev := range ch {
		if ev.Type == StreamEventText {
			text += ev.TextDelta
		}
	}
	if text != "ok" {
		t.Fatalf("text = %q", text)
	}
}

func TestFallbackPermanentErrPropagates(t *testing.T) {
	// 401 / auth errors should NOT trigger fallback.
	primary := &stubAdapter{name: "p", syncErr: errors.New("HTTP 401: invalid api key")}
	secondary := &stubAdapter{name: "s", events: []StreamEvent{{Type: StreamEventDone}}}
	chain := NewFallbackAdapter("test",
		FallbackEntry{Adapter: primary, Model: "p", Label: "p"},
		FallbackEntry{Adapter: secondary, Model: "s", Label: "s"},
	)
	_, err := chain.Stream(context.Background(), Request{})
	if err == nil {
		t.Fatal("expected auth error to propagate, got nil")
	}
}

func TestFallbackOnceContentEmitted(t *testing.T) {
	// Once content has streamed, mid-stream errors propagate without
	// triggering fallback (we can't unwind a partial response).
	primary := &stubAdapter{name: "p", events: []StreamEvent{
		{Type: StreamEventText, TextDelta: "first"},
		{Type: StreamEventError, Err: errors.New("503 then")},
	}}
	secondary := &stubAdapter{name: "s", events: []StreamEvent{
		{Type: StreamEventText, TextDelta: "second"},
		{Type: StreamEventDone},
	}}
	chain := NewFallbackAdapter("test",
		FallbackEntry{Adapter: primary, Model: "p", Label: "p"},
		FallbackEntry{Adapter: secondary, Model: "s", Label: "s"},
	)
	ch, _ := chain.Stream(context.Background(), Request{})
	var text string
	var sawErr bool
	for ev := range ch {
		switch ev.Type {
		case StreamEventText:
			text += ev.TextDelta
		case StreamEventError:
			sawErr = true
		}
	}
	if text != "first" {
		t.Fatalf("expected first only, got %q", text)
	}
	if !sawErr {
		t.Fatalf("expected stream error to propagate")
	}
}
