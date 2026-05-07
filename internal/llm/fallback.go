package llm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// FallbackEntry pairs an adapter with the model it should be invoked
// against. Different fallbacks typically use different model names
// (anthropic uses "claude-…", groq uses "llama-…"), so the chain
// rewrites Request.Model per attempt.
//
// FallbackModels lists alternative models to try on the same adapter
// before advancing to the next entry. Used when the primary model is
// unavailable (deprecated, unsupported on the provider) so we exhaust
// same-provider alternatives before switching providers.
//
// LiveModelFetcher, when non-nil, is called once (lazily, on first
// model-unsupported failure after FallbackModels are exhausted) to
// retrieve the provider's current live model list. Any IDs not already
// tried are appended to the rotation queue so deprecated hardcoded
// entries don't block progress. Use sync.Once internally if the fetch
// is expensive.
type FallbackEntry struct {
	Adapter          Adapter
	Model            string
	Label            string    // "<provider>/<model>" — for diagnostics
	FallbackModels   []string  // tried in order before advancing to next entry
	LiveModelFetcher func() []string // called at most once per run
}

// FallbackAdapter wraps an ordered chain of adapters. Stream tries each
// in turn, falling through to the next on retryable errors (rate
// limits, 5xx, connection failures, timeouts) — but only if the failing
// adapter hasn't yet emitted any text or tool calls. Once partial
// content has streamed, fallback stops; we can't unwind a partial
// response.
//
// OnFallback is invoked once per fall-through with the from / to labels
// and the trigger reason. Used by the TUI to flash a message and by
// telemetry to log the swap.
type FallbackAdapter struct {
	entries    []FallbackEntry
	name       string
	OnFallback func(from, to, reason string)
}

// NewFallbackAdapter builds a chain. Pass the primary first, fallbacks
// after. If only one entry is given, the chain behaves identically to
// the underlying adapter.
func NewFallbackAdapter(name string, entries ...FallbackEntry) *FallbackAdapter {
	return &FallbackAdapter{entries: entries, name: name}
}

// Entries returns the chain (read-only copy) for diagnostics.
func (f *FallbackAdapter) Entries() []FallbackEntry {
	out := make([]FallbackEntry, len(f.entries))
	copy(out, f.entries)
	return out
}

func (f *FallbackAdapter) Provider() string { return f.name }

func (f *FallbackAdapter) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	if len(f.entries) == 0 {
		return nil, errors.New("fallback adapter: no entries configured")
	}
	return f.tryFrom(ctx, 0, req)
}

// tryFrom attempts the adapter at idx. For each entry it cycles through
// FallbackModels (and, lazily, LiveModelFetcher results) on
// model-unsupported errors before advancing to the next entry.
func (f *FallbackAdapter) tryFrom(ctx context.Context, idx int, req Request) (<-chan StreamEvent, error) {
nextEntry:
	for i := idx; i < len(f.entries); i++ {
		entry := f.entries[i]
		// Index-based so we can append live-fetched models mid-loop.
		models := append([]string{entry.Model}, entry.FallbackModels...)
		liveFetched := false

		for mi := 0; mi < len(models); mi++ {
			model := models[mi]
			r := req
			r.Model = model
			ch, err := entry.Adapter.Stream(ctx, r)
			if err != nil {
				if isModelUnsupported(err) {
					if next, ok := f.nextModel(entry, models, mi, &liveFetched); ok {
						models = append(models, next...)
						f.notifyModelRotate(entry, model, models[mi+1], summariseErr(err))
						continue
					}
				}
				if isFallbackable(err) && i < len(f.entries)-1 {
					f.notify(i, i+1, summariseErr(err))
					continue nextEntry
				}
				return nil, err
			}
			// Peek the first event. If it's an error and we have more
			// options, fall through. Otherwise commit and replay.
			ev, ok, peekErr := peekFirst(ctx, ch)
			if peekErr != nil {
				return nil, peekErr
			}
			if !ok {
				// Channel closed before producing — treat as failure.
				if next, hasNext := f.nextModel(entry, models, mi, &liveFetched); hasNext {
					models = append(models, next...)
					f.notifyModelRotate(entry, model, models[mi+1], "stream closed without output")
					continue
				}
				if i < len(f.entries)-1 {
					f.notify(i, i+1, "stream closed without output")
					continue nextEntry
				}
				done := make(chan StreamEvent, 1)
				close(done)
				return done, nil
			}
			if ev.Type == StreamEventError && isFallbackable(ev.Err) {
				// OpenRouter 402: "can only afford N tokens". Retry the SAME
				// model once with MaxTokens capped to N before trying others,
				// so we don't lose the preferred model over a budget cap.
				if affordable := extractAffordableTokens(ev.Err.Error()); affordable >= 256 &&
					(r.MaxTokens == 0 || r.MaxTokens > affordable) {
					drain(ch)
					r2 := r
					r2.MaxTokens = affordable
					ch2, err2 := entry.Adapter.Stream(ctx, r2)
					if err2 == nil {
						ev2, ok2, _ := peekFirst(ctx, ch2)
						if ok2 && ev2.Type != StreamEventError {
							f.notify(i, i, fmt.Sprintf("token cap reduced to %d (402 budget limit)", affordable))
							return replay([]StreamEvent{ev2}, ch2), nil
						}
						if ok2 {
							drain(ch2)
						}
					}
				}
				// Model-unsupported: rotate within this provider first.
				if isModelUnsupported(ev.Err) {
					if next, hasNext := f.nextModel(entry, models, mi, &liveFetched); hasNext {
						drain(ch)
						models = append(models, next...)
						f.notifyModelRotate(entry, model, models[mi+1], summariseErr(ev.Err))
						continue
					}
				}
				if i < len(f.entries)-1 {
					f.notify(i, i+1, summariseErr(ev.Err))
					drain(ch)
					continue nextEntry
				}
			}
			return replay([]StreamEvent{ev}, ch), nil
		}
	}
	return nil, errors.New("fallback chain exhausted")
}

// nextModel returns any additional model IDs to append to the rotation
// queue. It returns (nil, true) when models[mi+1] already exists (no
// fetch needed), or calls LiveModelFetcher once and returns the new IDs
// not already in the tried set. Returns (nil, false) when no more
// candidates exist.
func (f *FallbackAdapter) nextModel(entry FallbackEntry, models []string, mi int, liveFetched *bool) (extra []string, hasNext bool) {
	if mi < len(models)-1 {
		return nil, true // already have more in the queue
	}
	if *liveFetched || entry.LiveModelFetcher == nil {
		return nil, false
	}
	*liveFetched = true
	tried := make(map[string]bool, len(models))
	for _, m := range models {
		tried[m] = true
	}
	live := entry.LiveModelFetcher()
	var fresh []string
	for _, id := range live {
		if !tried[id] {
			fresh = append(fresh, id)
		}
	}
	return fresh, len(fresh) > 0
}

func (f *FallbackAdapter) notify(fromIdx, toIdx int, reason string) {
	if f.OnFallback == nil {
		return
	}
	from := f.entries[fromIdx].Label
	to := f.entries[toIdx].Label
	f.OnFallback(from, to, reason)
}

// notifyModelRotate fires OnFallback when rotating to a different model on
// the same provider (before any cross-provider fallback).
func (f *FallbackAdapter) notifyModelRotate(entry FallbackEntry, fromModel, toModel, reason string) {
	if f.OnFallback == nil {
		return
	}
	// Derive the provider prefix from entry.Label ("<provider>/<model>").
	provider := entry.Label
	if idx := strings.LastIndex(provider, "/"); idx > 0 {
		provider = provider[:idx]
	}
	f.OnFallback(
		FormatLabel(provider, fromModel),
		FormatLabel(provider, toModel),
		reason,
	)
}

// peekFirst reads exactly one event from ch. Returns (ev, true, nil)
// on a real event, (zero, false, nil) on channel close, (zero, false,
// err) on context cancellation.
func peekFirst(ctx context.Context, ch <-chan StreamEvent) (StreamEvent, bool, error) {
	select {
	case <-ctx.Done():
		return StreamEvent{}, false, ctx.Err()
	case ev, ok := <-ch:
		return ev, ok, nil
	}
}

// drain consumes the rest of a channel without blocking the caller.
// Called when we've decided to fall through and don't want the
// abandoned adapter's goroutine stuck on send.
func drain(ch <-chan StreamEvent) {
	go func() {
		for range ch { //nolint:revive
		}
	}()
}

// replay yields the peeked events first, then forwards the rest of src.
// Returns a channel the caller owns.
func replay(peeked []StreamEvent, src <-chan StreamEvent) <-chan StreamEvent {
	out := make(chan StreamEvent, 8)
	go func() {
		defer close(out)
		for _, ev := range peeked {
			out <- ev
		}
		for ev := range src {
			out <- ev
		}
	}()
	return out
}

// isModelUnsupported returns true when the error indicates the specific
// model is unavailable on this provider (deprecated, not yet rolled out,
// or removed). It is a strict subset of isFallbackable: the difference is
// that model-unsupported errors trigger per-provider model rotation first;
// only after all FallbackModels for that entry are exhausted do they
// advance to the next FallbackEntry.
func isModelUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, h := range []string{
		"model not supported", "model_not_supported",
		"model not found", "modelnotfound",
		"modelerror",
		"not supported",
		"404", // provider returns 404 when the model ID doesn't exist
	} {
		if strings.Contains(msg, h) {
			return true
		}
	}
	return false
}

// isFallbackable returns true when an error warrants trying the next
// adapter in the chain. Retryable classes:
//   - HTTP 429 / rate-limit
//   - HTTP 402 Payment Required (OpenRouter: insufficient credits)
//   - HTTP 413 Request Entity Too Large (Groq / others: prompt too long)
//   - HTTP 5xx server errors
//   - Context-length / token-limit errors (various provider wordings)
//   - Network / connection failures
//   - Deadline exceeded
//
// Permanent errors (auth 401/403, 400 bad request, 404, schema
// mismatches) bubble up unchanged.
func isFallbackable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, h := range []string{
		"429", "rate limit", "rate-limit",
		"402", "payment required", "insufficient credits", "can only afford",
		"413", "request too large", "request entity too large",
		"context_length_exceeded", "context length exceeded",
		"maximum context length", "prompt is too long",
		"500", "502", "503", "504",
		"overloaded", "service unavailable", "temporarily unavailable",
		"connection refused", "connection reset", "broken pipe",
		"eof", "unexpected eof",
		"timeout", "timed out",
		"model not supported", "model_not_supported", "modelnotfound", "model not found",
		"not supported", "modelerror",
		"404", // model ID not found on provider
		"failed to call a function",
	} {
		if strings.Contains(msg, h) {
			return true
		}
	}
	return false
}

// extractAffordableTokens parses the token budget from an OpenRouter 402
// message like "… can only afford 3871 …" and returns it. Returns 0 when the
// pattern isn't present so callers can skip the token-reduction retry.
func extractAffordableTokens(errMsg string) int {
	const needle = "can only afford "
	idx := strings.Index(strings.ToLower(errMsg), needle)
	if idx < 0 {
		return 0
	}
	rest := errMsg[idx+len(needle):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// summariseErr extracts a short reason from an error message for the
// fallback notification line. Avoids dumping a full stack trace into
// the user's flash bar.
func summariseErr(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	if i := strings.Index(msg, "\n"); i > 0 {
		msg = msg[:i]
	}
	if len(msg) > 120 {
		msg = msg[:120] + "…"
	}
	return msg
}

// Compile-time check.
var _ Adapter = (*FallbackAdapter)(nil)

// helper: format a human label like "anthropic/claude-sonnet-4-6"
func FormatLabel(provider, model string) string {
	if provider == "" {
		return model
	}
	if model == "" {
		return provider
	}
	return fmt.Sprintf("%s/%s", provider, model)
}
