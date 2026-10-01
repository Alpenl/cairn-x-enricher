package processor

import (
	"context"
	"time"
)

type progressKey struct{}

// WithLaneProgress reports bounded task starts and completions to a scheduler.
// It never emits periodic pulses: a blocked dependency must become stale.
func WithLaneProgress(ctx context.Context, begin func(time.Duration) func()) context.Context {
	return context.WithValue(ctx, progressKey{}, begin)
}

func trackProgress(ctx context.Context, grace time.Duration) func() {
	if begin, ok := ctx.Value(progressKey{}).(func(time.Duration) func()); ok {
		return begin(grace)
	}
	return func() {}
}

func (p *Processor) claimGrace() time.Duration {
	return max(p.claimTimeout, 30*time.Second) + 30*time.Second
}
