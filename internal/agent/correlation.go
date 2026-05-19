package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

type correlationKey struct{}

var correlationSeq atomic.Uint64

func WithCorrelationID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

func CorrelationIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

func NewCorrelationID(prefix string) string {
	n := correlationSeq.Add(1)
	if prefix == "" {
		prefix = "corr"
	}
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), n)
}
