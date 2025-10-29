package cache

import (
	"context"
	"time"

	"go.uber.org/zap"
)

type Warmer struct {
	cache  *Cache
	logger *zap.Logger
}

func NewWarmer(cache *Cache, logger *zap.Logger) *Warmer {
	return &Warmer{
		cache:  cache,
		logger: logger,
	}
}

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
