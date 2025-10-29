package db

import (
	"context"
	"math"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type RetryConfig struct {
	MaxAttempts int
	InitialWait time.Duration
	MaxWait     time.Duration
	Multiplier  float64
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 5,
		InitialWait: 100 * time.Millisecond,
		MaxWait:     10 * time.Second,
		Multiplier:  2.0,
	}
}

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

func calculateBackoff(cfg RetryConfig, attempt int) time.Duration {
	wait := float64(cfg.InitialWait) * math.Pow(cfg.Multiplier, float64(attempt))
	jitter := rand.Float64() * 0.3 * wait
	wait = wait + jitter

	if wait > float64(cfg.MaxWait) {
		wait = float64(cfg.MaxWait)
	}

	return time.Duration(wait)
}

func isRetriable(err error) bool {
	return true
}

func NewWithRetry(cfg RetryConfig, dbConfig interface{}) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool
	err := WithRetry(context.Background(), cfg, func() error {
		var err error
		pool, err = pgxpool.NewWithConfig(context.Background(), dbConfig.(*pgxpool.Config))
		return err
	})
	return pool, err
}
