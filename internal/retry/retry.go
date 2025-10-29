package retry

import (
	"context"
	"math"
	"math/rand"
	"time"
)

type Config struct {
	MaxAttempts int
	InitialWait time.Duration
	MaxWait     time.Duration
	Multiplier  float64
}

func DefaultConfig() Config {
	return Config{
		MaxAttempts: 5,
		InitialWait: 100 * time.Millisecond,
		MaxWait:     10 * time.Second,
		Multiplier:  2.0,
	}
}

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

func calculateBackoffWithJitter(cfg Config, attempt int) time.Duration {
	base := float64(cfg.InitialWait) * math.Pow(cfg.Multiplier, float64(attempt))

	jitter := rand.Float64() * base * 0.3
	wait := base + jitter

	if wait > float64(cfg.MaxWait) {
		wait = float64(cfg.MaxWait)
	}

	return time.Duration(wait)
}
