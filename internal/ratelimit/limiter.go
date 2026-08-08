package ratelimit

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/metadata"
)

// Category names a class of methods that share a single rate-limit
// configuration.
type Category string

const (
	// CategoryExempt marks methods that are never rate limited.
	CategoryExempt Category = "exempt"
	// CategoryAuth covers authentication endpoints, which get the tightest limits.
	CategoryAuth Category = "auth"
	// CategoryMessage covers message send/edit operations.
	CategoryMessage Category = "message"
	// CategoryUpload covers file/avatar uploads.
	CategoryUpload Category = "upload"
	// CategoryEphemeral covers high-frequency, low-cost signals like typing and
	// read receipts, which are allowed a high rate.
	CategoryEphemeral Category = "ephemeral"
	// CategoryRead covers read-only Get/List/Search calls.
	CategoryRead Category = "read"
	// CategoryDefault is the fallback for any method not otherwise categorized.
	CategoryDefault Category = "default"
)

// LimitConfig defines the sustained rate (RequestsPerMinute) and short-term
// burst allowance for a Category.
type LimitConfig struct {
	RequestsPerMinute int
	Burst             int
}

// localEntry is a per-identity token bucket used by the in-process fallback,
// with lastSeen tracked so idle entries can be reaped by cleanup.
type localEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// Limiter enforces per-category, per-identity rate limits. It prefers a shared
// Redis token bucket and falls back to per-process buckets in local when Redis
// is unavailable. A background cleanup goroutine, started only when enabled,
// evicts idle local entries and is stopped by Close.
type Limiter struct {
	cache       *cache.Cache
	enabled     bool
	bypassToken string
	limits      map[Category]LimitConfig

	mu    sync.Mutex
	local map[string]*localEntry

	cleanupDone chan struct{}
	closeOnce   sync.Once
}

const (
	// localIdleTTL is how long a local token bucket may go unused before cleanup
	// removes it.
	localIdleTTL = 10 * time.Minute
	// cleanupInterval is how often the cleanup goroutine scans for idle buckets.
	cleanupInterval = 2 * time.Minute
)

// refills `rate` tokens/sec up to `capacity`, spends one, returns 1 if allowed
var tokenBucketScript = redis.NewScript(`
local tokens_key = KEYS[1]
local ts_key = KEYS[2]
local refill = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

local ttl = math.floor(capacity / refill * 2) + 10

local tokens = tonumber(redis.call("get", tokens_key))
if tokens == nil then
  tokens = capacity
end

local last = tonumber(redis.call("get", ts_key))
if last == nil then
  last = now
end

local delta = now - last
if delta < 0 then
  delta = 0
end

tokens = math.min(capacity, tokens + delta * refill)

local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end

redis.call("set", tokens_key, tokens, "EX", ttl)
redis.call("set", ts_key, now, "EX", ttl)

return allowed
`)

// NewLimiter constructs a Limiter seeded with per-category defaults derived from
// requestsPerMinute and burst. When enabled is true it starts the background
// cleanup goroutine (which Close later stops); a non-empty bypassToken enables
// metadata-based bypass via ShouldBypass.
func NewLimiter(cacheClient *cache.Cache, requestsPerMinute, burst int, enabled bool, bypassToken string) *Limiter {
	l := &Limiter{
		cache:       cacheClient,
		enabled:     enabled,
		bypassToken: strings.TrimSpace(bypassToken),
		limits:      defaultLimits(requestsPerMinute, burst),
		local:       make(map[string]*localEntry),
		cleanupDone: make(chan struct{}),
	}

	if enabled {
		go l.cleanup()
	}

	return l
}

// defaultLimits builds the per-category limit table. requestsPerMinute and
// burst supply CategoryDefault (falling back to 120/min and a quarter-rate burst
// when non-positive); the other categories use fixed tuned values.
func defaultLimits(requestsPerMinute, burst int) map[Category]LimitConfig {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 120
	}
	if burst <= 0 {
		burst = requestsPerMinute / 4
	}
	if burst < 1 {
		burst = 1
	}

	return map[Category]LimitConfig{
		CategoryDefault:   {RequestsPerMinute: requestsPerMinute, Burst: burst},
		CategoryAuth:      {RequestsPerMinute: 20, Burst: 5},
		CategoryMessage:   {RequestsPerMinute: 120, Burst: 30},
		CategoryUpload:    {RequestsPerMinute: 30, Burst: 5},
		CategoryEphemeral: {RequestsPerMinute: 600, Burst: 60},
		CategoryRead:      {RequestsPerMinute: 600, Burst: 120},
	}
}

// SetLimit overrides the configuration for a category. It ignores cfg values
// that are non-positive, so an invalid config cannot silently disable limiting.
// It is not safe to call concurrently with request handling.
func (l *Limiter) SetLimit(cat Category, cfg LimitConfig) {
	if cfg.RequestsPerMinute <= 0 || cfg.Burst <= 0 {
		return
	}
	l.limits[cat] = cfg
}

// Limit returns the LimitConfig for cat, falling back to the CategoryDefault
// config for any category without an explicit entry.
func (l *Limiter) Limit(cat Category) LimitConfig {
	if cfg, ok := l.limits[cat]; ok {
		return cfg
	}
	return l.limits[CategoryDefault]
}

// ShouldBypass reports whether the request carries a valid bypass token in
// BypassMetadataKey. It returns false when no bypass token is configured, and
// compares tokens in constant time to avoid leaking the token via timing. The
// bypass mechanism is intended only for voice-debug use.
func (l *Limiter) ShouldBypass(ctx context.Context) bool {
	if l.bypassToken == "" {
		return false
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}

	for _, candidate := range md.Get(BypassMetadataKey) {
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(candidate)), []byte(l.bypassToken)) == 1 {
			return true
		}
	}

	return false
}

// Allow reports whether a request from identity in category cat may proceed. It
// short-circuits to true when limiting is disabled or the category is exempt.
// It tries the shared Redis bucket first and, on any Redis error, falls back to
// the in-process bucket; the returned error is always nil.
func (l *Limiter) Allow(ctx context.Context, cat Category, identity string) (bool, error) {
	if !l.enabled || cat == CategoryExempt {
		return true, nil
	}

	cfg := l.Limit(cat)
	key := fmt.Sprintf("ratelimit:%s:%s", cat, identity)

	if l.cache != nil {
		allowed, err := l.allowRedis(ctx, key, cfg)
		if err == nil {
			return allowed, nil
		}
	}

	return l.allowLocal(key, cfg), nil
}

// allowRedis runs the atomic token-bucket Lua script against Redis, refilling at
// cfg.RequestsPerMinute per minute up to cfg.Burst, and reports whether a token
// was available. It returns the Redis error so the caller can fall back.
func (l *Limiter) allowRedis(ctx context.Context, key string, cfg LimitConfig) (bool, error) {
	refill := float64(cfg.RequestsPerMinute) / 60.0
	now := float64(time.Now().UnixNano()) / 1e9

	res, err := tokenBucketScript.Run(ctx, l.cache.Client(),
		[]string{key + ":tok", key + ":ts"},
		refill, cfg.Burst, now,
	).Int()
	if err != nil {
		return false, err
	}

	return res == 1, nil
}

// allowLocal applies the in-process token bucket for key, creating one on first
// use and refreshing its lastSeen. It only bounds this process, so with Redis
// down each server instance limits independently.
func (l *Limiter) allowLocal(key string, cfg LimitConfig) bool {
	l.mu.Lock()
	entry, ok := l.local[key]
	if !ok {
		entry = &localEntry{
			limiter: rate.NewLimiter(rate.Limit(float64(cfg.RequestsPerMinute)/60.0), cfg.Burst),
		}
		l.local[key] = entry
	}
	entry.lastSeen = time.Now()
	limiter := entry.limiter
	l.mu.Unlock()

	return limiter.Allow()
}

// Reset clears both the local and Redis buckets for a single category/identity,
// restoring that caller to full allowance. Returns any Redis deletion error.
func (l *Limiter) Reset(ctx context.Context, cat Category, identity string) error {
	key := fmt.Sprintf("ratelimit:%s:%s", cat, identity)

	l.mu.Lock()
	delete(l.local, key)
	l.mu.Unlock()

	if l.cache != nil {
		return l.cache.Delete(ctx, key+":tok", key+":ts")
	}

	return nil
}

// ClearAll drops every local bucket and deletes all "ratelimit:*" keys in Redis,
// resetting limits for all identities. Returns any Redis deletion error.
func (l *Limiter) ClearAll(ctx context.Context) error {
	l.mu.Lock()
	l.local = make(map[string]*localEntry)
	l.mu.Unlock()

	if l.cache != nil {
		return l.cache.DeletePattern(ctx, "ratelimit:*")
	}

	return nil
}

// cleanup runs in its own goroutine, periodically evicting local buckets whose
// lastSeen is older than localIdleTTL. It returns when the cleanupDone channel
// is closed by Close.
func (l *Limiter) cleanup() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-localIdleTTL)
			l.mu.Lock()
			for key, entry := range l.local {
				if entry.lastSeen.Before(cutoff) {
					delete(l.local, key)
				}
			}
			l.mu.Unlock()
		case <-l.cleanupDone:
			return
		}
	}
}

// Close stops the background cleanup goroutine. It is idempotent (guarded by
// closeOnce) and safe to call even when the limiter was created disabled.
func (l *Limiter) Close() {
	l.closeOnce.Do(func() {
		close(l.cleanupDone)
	})
}
