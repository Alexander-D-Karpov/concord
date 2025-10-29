package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
)

type LockoutManager struct {
	cache         *cache.Cache
	maxAttempts   int
	lockoutPeriod time.Duration
	attemptWindow time.Duration
}

func NewLockoutManager(cache *cache.Cache, maxAttempts int, lockoutPeriod, attemptWindow time.Duration) *LockoutManager {
	return &LockoutManager{
		cache:         cache,
		maxAttempts:   maxAttempts,
		lockoutPeriod: lockoutPeriod,
		attemptWindow: attemptWindow,
	}
}

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

func (lm *LockoutManager) IsLocked(ctx context.Context, identifier string) (bool, error) {
	lockKey := fmt.Sprintf("account_locked:%s", identifier)
	exists, err := lm.cache.Exists(ctx, lockKey)
	return exists, err
}

func (lm *LockoutManager) ClearAttempts(ctx context.Context, identifier string) error {
	key := fmt.Sprintf("login_attempts:%s", identifier)
	return lm.cache.Delete(ctx, key)
}
