package cache

import (
	"context"
	"errors"
	"time"
)

// AsidePattern implements the cache-aside strategy over a Cache: read from cache,
// fall back to a loader on a miss, and populate the cache for next time.
type AsidePattern struct {
	cache *Cache
}

// NewAsidePattern wraps cache with the cache-aside helpers.
func NewAsidePattern(cache *Cache) *AsidePattern {
	return &AsidePattern{cache: cache}
}

// GetOrLoad returns the cached value for key, or on a miss invokes loader,
// caches its result under key with ttl, and returns it. A miss is detected via
// errors.Is(err, ErrCacheMiss); any other cache error is returned directly.
// It does NOT dedupe concurrent loads, so a hot missing key can trigger a
// thundering herd of loader calls, and it ignores the Set error when populating
// the cache.
func (a *AsidePattern) GetOrLoad(ctx context.Context, key string, ttl time.Duration,
	loader func() (interface{}, error)) (interface{}, error) {
	var result interface{}
	err := a.cache.Get(ctx, key, &result)
	if err == nil {
		return result, nil
	}

	if !errors.Is(err, ErrCacheMiss) {
		return nil, err
	}

	result, err = loader()
	if err != nil {
		return nil, err
	}

	_ = a.cache.Set(ctx, key, result, ttl)
	return result, nil
}

// Invalidate deletes the given keys so the next GetOrLoad re-runs the loader.
func (a *AsidePattern) Invalidate(ctx context.Context, keys ...string) error {
	return a.cache.Delete(ctx, keys...)
}

// Get reads key into dest, returning ErrCacheMiss (via the underlying Cache) when
// the key is absent.
func (a *AsidePattern) Get(ctx context.Context, key string, dest interface{}) error {
	return a.cache.Get(ctx, key, dest)
}

// Set JSON-encodes value and stores it under key with the given ttl.
func (a *AsidePattern) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return a.cache.Set(ctx, key, value, ttl)
}

// Exists reports whether key is present in the cache.
func (a *AsidePattern) Exists(ctx context.Context, key string) (bool, error) {
	return a.cache.Exists(ctx, key)
}

// DeletePattern deletes every key matching the Redis glob pattern via a
// non-atomic SCAN + pipelined DEL.
func (a *AsidePattern) DeletePattern(ctx context.Context, pattern string) error {
	return a.cache.DeletePattern(ctx, pattern)
}
