package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// PoolMonitor periodically logs pgx pool statistics (connection counts, acquire
// timings) for observability. It runs a single background goroutine started by
// Start and stopped by Stop.
type PoolMonitor struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
	ticker *time.Ticker
	stop   chan struct{}
}

// NewPoolMonitor returns a monitor that will emit pool stats every interval once
// Start is called. It does not begin monitoring on its own.
func NewPoolMonitor(pool *pgxpool.Pool, logger *zap.Logger, interval time.Duration) *PoolMonitor {
	return &PoolMonitor{
		pool:   pool,
		logger: logger,
		ticker: time.NewTicker(interval),
		stop:   make(chan struct{}),
	}
}

// Start launches the monitoring goroutine, which logs pool stats on each tick
// and returns when Stop is called or ctx is cancelled. It does not block.
func (m *PoolMonitor) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-m.ticker.C:
				stats := m.pool.Stat()
				m.logger.Info("database pool stats",
					zap.Int32("total_conns", stats.TotalConns()),
					zap.Int32("idle_conns", stats.IdleConns()),
					zap.Int32("acquired_conns", stats.AcquiredConns()),
					zap.Int64("acquire_count", stats.AcquireCount()),
					zap.Duration("acquire_duration", stats.AcquireDuration()),
					zap.Int64("canceled_acquire_count", stats.CanceledAcquireCount()),
				)
			case <-m.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Stop halts the ticker and closes the stop channel, terminating the goroutine
// started by Start. It must be called at most once (a second call panics on the
// double close).
func (m *PoolMonitor) Stop() {
	m.ticker.Stop()
	close(m.stop)
}
