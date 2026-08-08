package db

import (
	"context"
	"math"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RetryConfig controls exponential-backoff retries: at most MaxAttempts tries,
// with the wait growing from InitialWait by Multiplier each attempt and capped
// at MaxWait.
type RetryConfig struct {
	MaxAttempts int
	InitialWait time.Duration
	MaxWait     time.Duration
	Multiplier  float64
}

// DefaultRetryConfig returns a sensible default: 5 attempts, 100ms initial wait
// doubling up to a 10s cap.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 5,
		InitialWait: 100 * time.Millisecond,
		MaxWait:     10 * time.Second,
		Multiplier:  2.0,
	}
}

// WithRetry calls fn until it succeeds, fn returns a non-retriable error, or
// cfg.MaxAttempts is exhausted. Between attempts it sleeps for the backoff delay
// but aborts early with ctx.Err() if ctx is cancelled. On exhaustion it returns
// the last error fn produced. Note isRetriable currently treats every error as
// retriable.
func WithRetry(ctx context.Context, cfg RetryConfig, fn func() error) error {
	var lastErr error

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			wait := calculateBackoff(cfg, attempt)
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
			if !isRetriable(err) {
				return err
			}
		}
	}

	return lastErr
}

// calculateBackoff returns the wait before the given attempt: InitialWait scaled
// by Multiplier^attempt, plus up to 30% random jitter to avoid thundering-herd
// synchronization, clamped to MaxWait.
func calculateBackoff(cfg RetryConfig, attempt int) time.Duration {
	wait := float64(cfg.InitialWait) * math.Pow(cfg.Multiplier, float64(attempt))
	jitter := rand.Float64() * 0.3 * wait
	wait = wait + jitter

	if wait > float64(cfg.MaxWait) {
		wait = float64(cfg.MaxWait)
	}

	return time.Duration(wait)
}

// isRetriable reports whether err warrants another attempt. It currently always
// returns true, treating every failure as transient; refine it to exclude
// permanent errors (e.g. auth failures) if needed.
func isRetriable(err error) bool {
	return true
}

// NewWithRetry opens a pgx pool from dbConfig (which must be a *pgxpool.Config),
// retrying pool creation under cfg's backoff policy. It returns the pool on the
// first success or the last error after exhausting attempts.
func NewWithRetry(cfg RetryConfig, dbConfig interface{}) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool
	err := WithRetry(context.Background(), cfg, func() error {
		var err error
		pool, err = pgxpool.NewWithConfig(context.Background(), dbConfig.(*pgxpool.Config))
		return err
	})
	return pool, err
}
