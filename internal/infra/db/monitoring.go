package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type PoolMonitor struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
	ticker *time.Ticker
	stop   chan struct{}
}

func NewPoolMonitor(pool *pgxpool.Pool, logger *zap.Logger, interval time.Duration) *PoolMonitor {
	return &PoolMonitor{
		pool:   pool,
		logger: logger,
		ticker: time.NewTicker(interval),
		stop:   make(chan struct{}),
	}
}

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

func (m *PoolMonitor) Stop() {
	m.ticker.Stop()
	close(m.stop)
}
