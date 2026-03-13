package ratelimit

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/metadata"
)

type Limiter struct {
	cache       *cache.Cache
	enabled     bool
	limits      map[string]LimitConfig
	localCache  map[string]*rate.Limiter
	mu          sync.RWMutex
	cleanupDone chan struct{}

	bypassToken string
}

type LimitConfig struct {
	RequestsPerMinute int
	Burst             int
}

func NewLimiter(cache *cache.Cache, requestsPerMinute, burst int, enabled bool, bypassToken string) *Limiter {
	l := &Limiter{
		cache:       cache,
		enabled:     enabled,
		bypassToken: strings.TrimSpace(bypassToken),
		limits: map[string]LimitConfig{
			"default": {
				RequestsPerMinute: requestsPerMinute,
				Burst:             burst,
			},
			"auth": {
				RequestsPerMinute: 10,
				Burst:             2,
			},
			"message": {
				RequestsPerMinute: 120,
				Burst:             20,
			},
			"upload": {
				RequestsPerMinute: 30,
				Burst:             5,
			},
		},
		localCache:  make(map[string]*rate.Limiter),
		cleanupDone: make(chan struct{}),
	}

	if enabled {
		go l.cleanup()
	}

	return l
}

func (l *Limiter) ShouldBypass(ctx context.Context) bool {
	if l.bypassToken == "" {
		return false
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}

	values := md.Get(BypassMetadataKey)
	if len(values) == 0 {
		return false
	}

	for _, candidate := range values {
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(candidate)), []byte(l.bypassToken)) == 1 {
			return true
		}
	}

	return false
}

func (l *Limiter) Allow(ctx context.Context, key string) (bool, error) {
	if !l.enabled {
		return true, nil
	}

	limitType := "default"
	if key == "auth" || key == "message" || key == "upload" {
		limitType = key
	}

	config := l.limits[limitType]

	if l.cache != nil {
		return l.allowRedis(ctx, key, config)
	}

	return l.allowLocal(key, config), nil
}

func (l *Limiter) allowLocal(key string, config LimitConfig) bool {
	l.mu.Lock()
	limiter, exists := l.localCache[key]
	if !exists {
		limit := rate.Limit(float64(config.RequestsPerMinute) / 60.0)
		limiter = rate.NewLimiter(limit, config.Burst)
		l.localCache[key] = limiter
	}
	l.mu.Unlock()

	return limiter.Allow()
}

func (l *Limiter) allowRedis(ctx context.Context, key string, config LimitConfig) (bool, error) {
	cacheKey := fmt.Sprintf("ratelimit:%s", key)

	count, err := l.cache.Incr(ctx, cacheKey)
	if err != nil {
		return l.allowLocal(key, config), nil
	}

	if count == 1 {
		_ = l.cache.Expire(ctx, cacheKey, time.Minute)
	}

	if count > int64(config.RequestsPerMinute) {
		return false, nil
	}

	return true, nil
}

func (l *Limiter) Reset(ctx context.Context, key string) error {
	l.mu.Lock()
	delete(l.localCache, key)
	l.mu.Unlock()

	if l.cache != nil {
		cacheKey := fmt.Sprintf("ratelimit:%s", key)
		return l.cache.Delete(ctx, cacheKey)
	}

	return nil
}

func (l *Limiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.mu.Lock()
			for key := range l.localCache {
				l.localCache[key] = nil
			}
			l.localCache = make(map[string]*rate.Limiter)
			l.mu.Unlock()
		case <-l.cleanupDone:
			return
		}
	}
}

func (l *Limiter) Close() {
	close(l.cleanupDone)
}

func (l *Limiter) ClearAll(ctx context.Context) error {
	l.mu.Lock()
	l.localCache = make(map[string]*rate.Limiter)
	l.mu.Unlock()

	if l.cache != nil {
		pattern := "ratelimit:*"
		return l.cache.DeletePattern(ctx, pattern)
	}

	return nil
}
