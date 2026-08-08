package testutil

import (
	"strconv"
	"sync"
	"testing"

	infraCache "github.com/Alexander-D-Karpov/concord/internal/infra/cache"
)

// Package-level singletons back a single Redis connection shared across the whole
// test binary: cacheOnce and asideOnce ensure the cache and the cache-aside helper
// are each built at most once per process, sharedCache and sharedAside hold those
// instances, and cacheErr records a connection failure so every caller can degrade
// consistently.
var (
	cacheOnce   sync.Once
	asideOnce   sync.Once
	sharedCache *infraCache.Cache
	sharedAside *infraCache.AsidePattern
	cacheErr    error
)

// GetCache returns the process-wide shared Redis cache, connecting once on the first
// call using the REDIS_HOST/REDIS_PORT/REDIS_PASSWORD/REDIS_DB env vars (defaulting
// to localhost:6379, db 0). When Redis is unreachable it logs and returns nil rather
// than failing the test, so cache-optional tests can continue; use MustCache when the
// cache is mandatory.
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

// MustCache returns the shared cache like GetCache, but fails the test via t.Fatalf
// when Redis is unavailable; use it in tests that cannot run without a cache.
func MustCache(t *testing.T) *infraCache.Cache {
	t.Helper()
	c := GetCache(t)
	if c == nil {
		t.Fatalf("Redis is required for this test but not available")
	}
	return c
}

// GetAside returns the process-wide shared cache-aside helper, built once over the
// shared cache on first call. It returns nil (and builds nothing) when the underlying
// Redis cache is unavailable.
func GetAside(t *testing.T) *infraCache.AsidePattern {
	t.Helper()

	asideOnce.Do(func() {
		if c := GetCache(t); c != nil {
			sharedAside = infraCache.NewAsidePattern(c)
		}
	})

	return sharedAside
}

// MustAside returns the shared AsidePattern like GetAside, but fails the test via
// t.Fatalf when Redis (and therefore the aside helper) is unavailable.
func MustAside(t *testing.T) *infraCache.AsidePattern {
	t.Helper()
	a := GetAside(t)
	if a == nil {
		t.Fatalf("Redis AsidePattern is required for this test but not available")
	}
	return a
}

// CacheFlushAll flushes every key from the shared Redis instance so a test starts
// from clean state. It is a no-op when the cache was never connected and logs (rather
// than fails) on error. Because it wipes the entire selected database, point tests at
// a dedicated REDIS_DB.
func CacheFlushAll(t *testing.T) {
	t.Helper()
	if sharedCache != nil {
		if err := sharedCache.FlushAll(t.Context()); err != nil {
			t.Logf("testutil: FlushAll failed: %v", err)
		}
	}
}

// CacheTeardown closes the shared Redis connection and clears the cache singletons;
// it is meant to run once (e.g. from TestMain) after all tests finish. It does not
// reset the sync.Once guards, so no reconnection happens afterward within the same
// process.
func CacheTeardown() {
	if sharedCache != nil {
		_ = sharedCache.Close()
		sharedCache = nil
	}
	sharedAside = nil
}
