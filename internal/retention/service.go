// Package retention enforces per-room message retention by soft-deleting messages
// older than each room's configured retention window.
package retention

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Service purges expired messages according to each room's retention_days setting.
type Service struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
}

// NewService builds a retention Service over the given pool.
func NewService(pool *pgxpool.Pool, logger *zap.Logger) *Service {
	return &Service{pool: pool, logger: logger}
}

// PurgeOnce soft-deletes (sets deleted_at) every not-yet-deleted message whose age
// exceeds its room's retention_days, across all rooms with retention enabled
// (retention_days > 0). It returns the number of messages purged. Rooms with
// retention_days = 0 (the default) are left untouched.
func (s *Service) PurgeOnce(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE messages AS m
		SET deleted_at = NOW()
		FROM room_settings AS rs
		WHERE m.room_id = rs.room_id
		  AND rs.retention_days > 0
		  AND m.deleted_at IS NULL
		  AND m.created_at < NOW() - make_interval(days => rs.retention_days)
	`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RunPurger runs PurgeOnce on a fixed interval until ctx is cancelled. It logs the
// count purged when non-zero and logs (but does not stop on) errors, so a transient
// database issue does not kill the loop.
func (s *Service) RunPurger(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.PurgeOnce(ctx)
			if err != nil {
				s.logger.Warn("retention purge failed", zap.Error(err))
				continue
			}
			if n > 0 {
				s.logger.Info("retention purge", zap.Int64("messages_purged", n))
			}
		}
	}
}
