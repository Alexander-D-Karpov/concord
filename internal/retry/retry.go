package retry

import (
	"context"
	"math"
	"math/rand"
	"time"
)

// Config controls retry behavior: the maximum number of attempts, the initial
// backoff wait, the ceiling on any single wait (MaxWait), and the exponential
// growth factor (Multiplier) applied between attempts.
type Config struct {
	MaxAttempts int
	InitialWait time.Duration
	MaxWait     time.Duration
	Multiplier  float64
}

// DefaultConfig returns a reasonable retry policy: up to 5 attempts starting at
// 100ms, doubling each time, capped at 10s per wait.
func DefaultConfig() Config {
	return Config{
		MaxAttempts: 5,
		InitialWait: 100 * time.Millisecond,
		MaxWait:     10 * time.Second,
		Multiplier:  2.0,
	}
}

// WithBackoff calls fn repeatedly until it returns nil or cfg.MaxAttempts is
// reached, waiting with jittered exponential backoff between attempts. It
// returns nil on the first success, ctx.Err() if the context is cancelled
// during a wait, or the last error from fn once attempts are exhausted.
func WithBackoff(ctx context.Context, cfg Config, fn func() error) error {
	var lastErr error

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			wait := calculateBackoffWithJitter(cfg, attempt)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return lastErr
}

// calculateBackoffWithJitter returns the wait before the given attempt:
// InitialWait scaled by Multiplier^attempt, plus up to 30% random jitter to
// avoid synchronized retries, clamped to cfg.MaxWait.
func calculateBackoffWithJitter(cfg Config, attempt int) time.Duration {
	base := float64(cfg.InitialWait) * math.Pow(cfg.Multiplier, float64(attempt))

	jitter := rand.Float64() * base * 0.3
	wait := base + jitter

	if wait > float64(cfg.MaxWait) {
		wait = float64(cfg.MaxWait)
	}

	return time.Duration(wait)
}
