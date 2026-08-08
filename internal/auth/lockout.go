package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
)

// LockoutManager throttles brute-force login attempts using the cache as a counter
// store. It counts failures per identifier within attemptWindow and, once
// maxAttempts is reached, locks the identifier for lockoutPeriod.
type LockoutManager struct {
	cache         *cache.Cache
	maxAttempts   int
	lockoutPeriod time.Duration
	attemptWindow time.Duration
}

// NewLockoutManager builds a LockoutManager: maxAttempts failures within
// attemptWindow trip a lock lasting lockoutPeriod, all tracked in cache.
func NewLockoutManager(cache *cache.Cache, maxAttempts int, lockoutPeriod, attemptWindow time.Duration) *LockoutManager {
	return &LockoutManager{
		cache:         cache,
		maxAttempts:   maxAttempts,
		lockoutPeriod: lockoutPeriod,
		attemptWindow: attemptWindow,
	}
}

// RecordFailedAttempt increments the failure counter for identifier, setting the
// attemptWindow expiry on the first failure so the window slides from that point.
// When the count reaches maxAttempts it writes a lock key with lockoutPeriod TTL.
func (lm *LockoutManager) RecordFailedAttempt(ctx context.Context, identifier string) error {
	key := fmt.Sprintf("login_attempts:%s", identifier)

	count, err := lm.cache.Incr(ctx, key)
	if err != nil {
		return err
	}

	if count == 1 {
		_ = lm.cache.Expire(ctx, key, lm.attemptWindow)
	}

	if count >= int64(lm.maxAttempts) {
		lockKey := fmt.Sprintf("account_locked:%s", identifier)
		return lm.cache.Set(ctx, lockKey, true, lm.lockoutPeriod)
	}

	return nil
}

// IsLocked reports whether identifier currently has an active lock. The lock
// expires on its own once lockoutPeriod elapses.
func (lm *LockoutManager) IsLocked(ctx context.Context, identifier string) (bool, error) {
	lockKey := fmt.Sprintf("account_locked:%s", identifier)
	exists, err := lm.cache.Exists(ctx, lockKey)
	return exists, err
}

// ClearAttempts deletes the failure counter for identifier, typically called after
// a successful login so past failures don't count toward a future lockout. It does
// not clear an already-active lock.
func (lm *LockoutManager) ClearAttempts(ctx context.Context, identifier string) error {
	key := fmt.Sprintf("login_attempts:%s", identifier)
	return lm.cache.Delete(ctx, key)
}
