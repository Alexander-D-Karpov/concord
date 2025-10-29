package authz

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
)

type PermissionCache struct {
	cache *cache.Cache
	rbac  *RBAC
	ttl   time.Duration
}

func NewPermissionCache(cache *cache.Cache, rbac *RBAC, ttl time.Duration) *PermissionCache {
	return &PermissionCache{
		cache: cache,
		rbac:  rbac,
		ttl:   ttl,
	}
}

func (pc *PermissionCache) HasPermission(ctx context.Context, userID, resourceID string, permission Permission) (bool, error) {
	cacheKey := fmt.Sprintf("perm:%s:%s:%s", userID, resourceID, permission)

	var hasPermission bool
	err := pc.cache.Get(ctx, cacheKey, &hasPermission)
	if err == nil {
		return hasPermission, nil
	}

	hasPermission = pc.rbac.HasPermission(ctx, userID, resourceID, permission)

	_ = pc.cache.Set(ctx, cacheKey, hasPermission, pc.ttl)

	return hasPermission, nil
}

func (pc *PermissionCache) InvalidateUser(ctx context.Context, userID string) error {
	return pc.cache.Delete(ctx, fmt.Sprintf("perm:%s:*", userID))
}
