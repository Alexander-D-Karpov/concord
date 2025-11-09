package testutil

import (
	"strconv"
	"sync"
	"testing"

	infraCache "github.com/Alexander-D-Karpov/concord/internal/infra/cache"
)

var (
	cacheOnce   sync.Once
	asideOnce   sync.Once
	sharedCache *infraCache.Cache
	sharedAside *infraCache.AsidePattern
	cacheErr    error
)

func GetCache(t *testing.T) *infraCache.Cache {
	t.Helper()

	cacheOnce.Do(func() {
		host := envOr("REDIS_HOST", "localhost")

		portStr := envOr("REDIS_PORT", "6379")
		port, err := strconv.Atoi(portStr)
		if err != nil {
			port = 6379
		}

		pw := envOr("REDIS_PASSWORD", "")

		dbStr := envOr("REDIS_DB", "0")
		dbNum, err := strconv.Atoi(dbStr)
		if err != nil {
			dbNum = 0
		}

		sharedCache, cacheErr = infraCache.New(host, port, pw, dbNum)
	})

	if cacheErr != nil {
		t.Logf("testutil: Redis cache not available (%v); proceeding without cache", cacheErr)
		return nil
	}
	return sharedCache
}

func MustCache(t *testing.T) *infraCache.Cache {
	t.Helper()
	c := GetCache(t)
	if c == nil {
		t.Fatalf("Redis is required for this test but not available")
	}
	return c
}

func GetAside(t *testing.T) *infraCache.AsidePattern {
	t.Helper()

	asideOnce.Do(func() {
		if c := GetCache(t); c != nil {
			sharedAside = infraCache.NewAsidePattern(c)
		}
	})

	return sharedAside
}

func MustAside(t *testing.T) *infraCache.AsidePattern {
	t.Helper()
	a := GetAside(t)
	if a == nil {
		t.Fatalf("Redis AsidePattern is required for this test but not available")
	}
	return a
}

func CacheFlushAll(t *testing.T) {
	t.Helper()
	if sharedCache != nil {
		if err := sharedCache.FlushAll(t.Context()); err != nil {
			t.Logf("testutil: FlushAll failed: %v", err)
		}
	}
}

func CacheTeardown() {
	if sharedCache != nil {
		_ = sharedCache.Close()
		sharedCache = nil
	}
	sharedAside = nil
}
