package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"golang.org/x/time/rate"
)

type Limiter struct {
	cache      *cache.Cache
	rps        int
	burst      int
	localCache map[string]*rate.Limiter
	enabled    bool
}

func NewLimiter(cache *cache.Cache, requestsPerMinute, burst int, enabled bool) *Limiter {
	return &Limiter{
		cache:      cache,
		rps:        requestsPerMinute / 60,
		burst:      burst,
		localCache: make(map[string]*rate.Limiter),
		enabled:    enabled,
	}
}

func (l *Limiter) Allow(ctx context.Context, key string) (bool, error) {
	if !l.enabled {
		return true, nil
	}

	limiter, exists := l.localCache[key]
	if !exists {
		limiter = rate.NewLimiter(rate.Limit(l.rps), l.burst)
		l.localCache[key] = limiter
	}

	if !limiter.Allow() {
		return false, nil
	}

	if l.cache != nil {
		cacheKey := fmt.Sprintf("ratelimit:%s", key)
		count, err := l.cache.Incr(ctx, cacheKey)
		if err != nil {
			return true, nil
		}

		if count == 1 {
			_ = l.cache.Expire(ctx, cacheKey, time.Minute)
		}

		if count > int64(l.rps*60) {
			return false, nil
		}
	}

	return true, nil
}

func (l *Limiter) Reset(ctx context.Context, key string) error {
	delete(l.localCache, key)

	if l.cache != nil {
		cacheKey := fmt.Sprintf("ratelimit:%s", key)
		return l.cache.Delete(ctx, cacheKey)
	}

	return nil
}
