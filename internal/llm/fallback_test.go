package llm

import (
	"context"
	"errors"
	"testing"
)

type stubAdapter struct {
	name         string
	syncErr      error         // returned synchronously from Stream
	events       []StreamEvent // emitted on the channel after Stream returns
	gotModel     string        // captures the Model field of the last request
	gotMaxTokens int           // captures the MaxTokens field of the last request
	streamErr    error         // emitted as a StreamEventError BEFORE events
	// calls tracks each (model, maxTokens) pair so tests can assert
	// that retry attempts used the expected parameters.
	calls []struct{ model string; maxTokens int }
}

func (s *stubAdapter) Provider() string { return s.name }
func (s *stubAdapter) Stream(_ context.Context, req Request) (<-chan StreamEvent, error) {
	s.gotModel = req.Model
	s.gotMaxTokens = req.MaxTokens
	s.calls = append(s.calls, struct{ model string; maxTokens int }{req.Model, req.MaxTokens})
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

func TestExtractAffordableTokens(t *testing.T) {
	cases := []struct {
		msg  string
		want int
	}{
		{`This request requires more credits, or fewer max tokens. You requested up to 16384 tokens, but can only afford 3871. To increase, visit https://openrouter.ai/credits`, 3871},
		{`can only afford 500`, 500},
		{`CAN ONLY AFFORD 1024 tokens remaining`, 1024},
		{`HTTP 402: payment required`, 0},        // no count
		{`rate limit exceeded`, 0},                // different error
		{`can only afford 0`, 0},                  // zero is not useful
	}
	for _, c := range cases {
		got := extractAffordableTokens(c.msg)
		if got != c.want {
			t.Errorf("extractAffordableTokens(%q) = %d, want %d", c.msg, got, c.want)
		}
	}
}

// retryingStub returns a 402 on the first call and succeeds on the second, so
// we can assert that tryFrom retried with a reduced token cap.
type retryingStub struct {
	name      string
	callCount int
	captured  []Request
}

func (r *retryingStub) Provider() string { return r.name }
func (r *retryingStub) Stream(_ context.Context, req Request) (<-chan StreamEvent, error) {
	r.callCount++
	r.captured = append(r.captured, req)
	ch := make(chan StreamEvent, 4)
	if r.callCount == 1 {
		// First call: 402 with an affordable count in the message.
		ch <- StreamEvent{
			Type: StreamEventError,
			Err:  errors.New(`402 Payment Required: can only afford 1024 tokens`),
		}
		close(ch)
		return ch, nil
	}
	// Subsequent calls: success.
	ch <- StreamEvent{Type: StreamEventText, TextDelta: "retried-ok"}
	ch <- StreamEvent{Type: StreamEventDone}
	close(ch)
	return ch, nil
}

func TestFallback402RetryWithFewerTokens(t *testing.T) {
	primary := &retryingStub{name: "openrouter"}
	secondary := &stubAdapter{name: "backup", events: []StreamEvent{
		{Type: StreamEventText, TextDelta: "fallback-used"},
		{Type: StreamEventDone},
	}}
	var notified []string
	chain := NewFallbackAdapter("test",
		FallbackEntry{Adapter: primary, Model: "big-model", Label: "openrouter/big-model"},
		FallbackEntry{Adapter: secondary, Model: "s-model", Label: "backup/s-model"},
	)
	chain.OnFallback = func(from, to, reason string) { notified = append(notified, from+"->"+to) }

	ch, err := chain.Stream(context.Background(), Request{Model: "big-model", MaxTokens: 16384})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var text string
	for ev := range ch {
		if ev.Type == StreamEventText {
			text += ev.TextDelta
		}
	}
	// Should have used the same provider (retry), NOT the secondary.
	if text != "retried-ok" {
		t.Fatalf("text = %q, want %q", text, "retried-ok")
	}
	if secondary.gotModel != "" {
		t.Fatalf("secondary was invoked unexpectedly with model %q", secondary.gotModel)
	}
	// Retry must have used MaxTokens = 1024.
	if primary.callCount != 2 {
		t.Fatalf("primary call count = %d, want 2", primary.callCount)
	}
	if primary.captured[1].MaxTokens != 1024 {
		t.Fatalf("retry MaxTokens = %d, want 1024", primary.captured[1].MaxTokens)
	}
}

func TestFallback402FallsBackWhenRetryAlsoFails(t *testing.T) {
	// Primary always returns 402 — reduced-token retry also fails.
	primary := &stubAdapter{
		name:      "openrouter",
		streamErr: errors.New(`402 Payment Required: can only afford 1024 tokens`),
	}
	secondary := &stubAdapter{name: "backup", events: []StreamEvent{
		{Type: StreamEventText, TextDelta: "backup-ok"},
		{Type: StreamEventDone},
	}}
	chain := NewFallbackAdapter("test",
		FallbackEntry{Adapter: primary, Model: "big-model", Label: "openrouter/big-model"},
		FallbackEntry{Adapter: secondary, Model: "s-model", Label: "backup/s-model"},
	)

	ch, err := chain.Stream(context.Background(), Request{Model: "big-model", MaxTokens: 16384})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var text string
	for ev := range ch {
		if ev.Type == StreamEventText {
			text += ev.TextDelta
		}
	}
	if text != "backup-ok" {
		t.Fatalf("text = %q, want %q", text, "backup-ok")
	}
}

func TestFallback402NoAffordableCountFallsBackDirectly(t *testing.T) {
	// 402 with no parseable token count — fall back immediately, no retry.
	primary := &stubAdapter{
		name:      "openrouter",
		streamErr: errors.New(`402 Payment Required: insufficient credits`),
	}
	secondary := &stubAdapter{name: "backup", events: []StreamEvent{
		{Type: StreamEventText, TextDelta: "backup-ok"},
		{Type: StreamEventDone},
	}}
	chain := NewFallbackAdapter("test",
		FallbackEntry{Adapter: primary, Model: "big-model", Label: "openrouter/big-model"},
		FallbackEntry{Adapter: secondary, Model: "s-model", Label: "backup/s-model"},
	)

	ch, err := chain.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var text string
	for ev := range ch {
		if ev.Type == StreamEventText {
			text += ev.TextDelta
		}
	}
	if text != "backup-ok" {
		t.Fatalf("text = %q, want %q", text, "backup-ok")
	}
	// Primary was called exactly once (no retry attempt).
	if len(primary.calls) != 1 {
		t.Fatalf("primary calls = %d, want 1", len(primary.calls))
	}
}

func TestModelRotationBeforeProviderFallback(t *testing.T) {
	// entry[0] has Model="model-a", FallbackModels=["model-b"].
	// Stream("model-a") → not-supported. Stream("model-b") → success.
	// Secondary provider must NOT be called.
	var modelAErr = errors.New("401 ModelError: model-a not supported")
	rotatingAdapter := &retryingStubForModel{
		badModel: "model-a",
		badErr:   modelAErr,
		goodText: "rotated-ok",
	}
	secondary := &stubAdapter{name: "secondary", events: []StreamEvent{
		{Type: StreamEventText, TextDelta: "should-not-reach"},
	}}
	var notified []string
	chain := NewFallbackAdapter("test",
		FallbackEntry{Adapter: rotatingAdapter, Model: "model-a", Label: "p/model-a", FallbackModels: []string{"model-b"}},
		FallbackEntry{Adapter: secondary, Model: "s-model", Label: "secondary/s-model"},
	)
	chain.OnFallback = func(from, to, reason string) { notified = append(notified, from+"->"+to) }

	ch, err := chain.Stream(context.Background(), Request{Model: "model-a"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var text string
	for ev := range ch {
		if ev.Type == StreamEventText {
			text += ev.TextDelta
		}
	}
	if text != "rotated-ok" {
		t.Fatalf("text = %q, want %q", text, "rotated-ok")
	}
	if secondary.gotModel != "" {
		t.Fatalf("secondary provider was invoked unexpectedly")
	}
	if len(notified) != 1 || notified[0] != "p/model-a->p/model-b" {
		t.Fatalf("OnFallback = %v, want [p/model-a->p/model-b]", notified)
	}
	if rotatingAdapter.lastModel != "model-b" {
		t.Fatalf("last model used = %q, want model-b", rotatingAdapter.lastModel)
	}
}

// retryingStubForModel returns a model-unsupported error for badModel and
// success for any other model.
type retryingStubForModel struct {
	badModel  string
	badErr    error
	goodText  string
	lastModel string
}

func (r *retryingStubForModel) Provider() string { return "p" }
func (r *retryingStubForModel) Stream(_ context.Context, req Request) (<-chan StreamEvent, error) {
	r.lastModel = req.Model
	ch := make(chan StreamEvent, 4)
	if req.Model == r.badModel {
		ch <- StreamEvent{Type: StreamEventError, Err: r.badErr}
		close(ch)
		return ch, nil
	}
	ch <- StreamEvent{Type: StreamEventText, TextDelta: r.goodText}
	ch <- StreamEvent{Type: StreamEventDone}
	close(ch)
	return ch, nil
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
