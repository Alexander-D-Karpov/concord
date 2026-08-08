package cache

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Warmer pre-populates the cache with data (e.g. user profiles) so the first
// real request hits a warm cache instead of the origin store.
type Warmer struct {
	cache  *Cache
	logger *zap.Logger
}

// NewWarmer returns a Warmer that writes into cache and logs failures to logger.
func NewWarmer(cache *Cache, logger *zap.Logger) *Warmer {
	return &Warmer{
		cache:  cache,
		logger: logger,
	}
}

// WarmUserProfiles loads each user via loader and caches it under "user:<id>"
// with a 5-minute TTL. Per-user load or cache errors are logged and skipped
// rather than aborting the batch, so it always returns nil.
func (w *Warmer) WarmUserProfiles(ctx context.Context, userIDs []string, loader func(string) (interface{}, error)) error {
	for _, userID := range userIDs {
		data, err := loader(userID)
		if err != nil {
			w.logger.Warn("failed to load user profile", zap.String("user_id", userID), zap.Error(err))
			continue
		}

		if err := w.cache.Set(ctx, "user:"+userID, data, 5*time.Minute); err != nil {
			w.logger.Warn("failed to cache user profile", zap.String("user_id", userID), zap.Error(err))
		}
	}

	return nil
}
