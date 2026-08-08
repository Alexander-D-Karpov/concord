package slowmode

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// configTTL is how long a room's slow-mode interval is cached before it is
	// re-read from the database.
	configTTL = 60 * time.Second
)

// Service reads and enforces per-room slow mode, backed by the rooms table for the
// interval and by the cache for both the cached interval and per-user last-sent
// timestamps. When cache is nil, enforcement is disabled (see CheckAndStamp).
type Service struct {
	pool  *pgxpool.Pool
	cache *cache.AsidePattern
}

// NewService returns a Service reading from pool and using aside for caching.
// aside may be nil, in which case slow mode is effectively off.
func NewService(pool *pgxpool.Pool, aside *cache.AsidePattern) *Service {
	return &Service{pool: pool, cache: aside}
}

// configKey is the cache key holding a room's slow-mode interval.
func configKey(roomID uuid.UUID) string {
	return fmt.Sprintf("room:slowmode:cfg:%s", roomID)
}

// lastSentKey is the cache key holding the Unix timestamp of a user's last send in
// a room; its TTL is the slow-mode interval, so expiry means the cooldown passed.
func lastSentKey(roomID, userID uuid.UUID) string {
	return fmt.Sprintf("room:slowmode:last:%s:%s", roomID, userID)
}

// Get returns the room's slow-mode interval in seconds (0 = disabled), reading the
// cache first and falling back to the rooms table. A missing room reports 0 rather
// than an error. On a database read it populates the cache for configTTL.
func (s *Service) Get(ctx context.Context, roomID uuid.UUID) (int, error) {
	if s.cache != nil {
		var v int
		if err := s.cache.Get(ctx, configKey(roomID), &v); err == nil {
			return v, nil
		}
	}
	var interval int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(slow_mode_interval, 0) FROM rooms WHERE id = $1`, roomID,
	).Scan(&interval)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if s.cache != nil {
		_ = s.cache.Set(ctx, configKey(roomID), interval, configTTL)
	}
	return interval, nil
}

// Set updates the room's slow-mode interval (seconds) and invalidates the cached
// interval so the next Get reflects the change.
func (s *Service) Set(ctx context.Context, roomID uuid.UUID, interval int32) error {
	_, err := s.pool.Exec(ctx, `UPDATE rooms SET slow_mode_interval = $2 WHERE id = $1`, roomID, interval)
	if err != nil {
		return err
	}
	if s.cache != nil {
		_ = s.cache.Invalidate(ctx, configKey(roomID))
	}
	return nil
}

// CheckAndStamp enforces slow mode for a single send: it returns remaining > 0 (in
// seconds) if the user is still cooling down and must be blocked, or 0 if the send
// is allowed. When allowed, it records the send time as a side effect, so calling
// it more than once per message double-stamps and starts an extra cooldown. Admins
// and moderators (exemptRole) always return 0, as do rooms with slow mode off or a
// nil cache.
func (s *Service) CheckAndStamp(ctx context.Context, roomID, userID uuid.UUID, exemptRole string) (remaining int64, err error) {
	if exemptRole == "admin" || exemptRole == "moderator" {
		return 0, nil
	}
	interval, err := s.Get(ctx, roomID)
	if err != nil {
		return 0, err
	}
	if interval == 0 {
		return 0, nil
	}
	if s.cache == nil {
		return 0, nil
	}
	key := lastSentKey(roomID, userID)
	var lastSent int64
	if err := s.cache.Get(ctx, key, &lastSent); err == nil {
		rem := int64(interval) - (time.Now().Unix() - lastSent)
		if rem > 0 {
			return rem, nil
		}
	}
	_ = s.cache.Set(ctx, key, time.Now().Unix(), time.Duration(interval)*time.Second)
	return 0, nil
}
