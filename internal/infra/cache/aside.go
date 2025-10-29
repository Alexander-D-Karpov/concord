package cache

import (
	"context"
	"time"
)

type AsidePattern struct {
	cache *Cache
}

func NewAsidePattern(cache *Cache) *AsidePattern {
	return &AsidePattern{cache: cache}
}

func (a *AsidePattern) GetOrLoad(ctx context.Context, key string, ttl time.Duration, loader func() (interface{}, error)) (interface{}, error) {
	var result interface{}
	err := a.cache.Get(ctx, key, &result)
	if err == nil {
		return result, nil
	}

	if err != ErrCacheMiss {
		return nil, err
	}

	result, err = loader()
	if err != nil {
		return nil, err
	}

	_ = a.cache.Set(ctx, key, result, ttl)
	return result, nil
}

func (a *AsidePattern) Invalidate(ctx context.Context, keys ...string) error {
	return a.cache.Delete(ctx, keys...)
}
