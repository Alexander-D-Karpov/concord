package authz

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
)

// PermissionCache is a read-through cache in front of RBAC permission checks. It
// memoizes each (user, resource, permission) decision for ttl to avoid repeated
// RBAC evaluation. Because results are cached, a role change is not reflected until
// the entry expires or InvalidateUser is called.
type PermissionCache struct {
	cache *cache.Cache
	rbac  *RBAC
	ttl   time.Duration
}

// NewPermissionCache returns a PermissionCache caching rbac decisions in cache for ttl.
func NewPermissionCache(cache *cache.Cache, rbac *RBAC, ttl time.Duration) *PermissionCache {
	return &PermissionCache{
		cache: cache,
		rbac:  rbac,
		ttl:   ttl,
	}
}

// HasPermission reports whether the user holds permission on the resource, serving
// a cached decision when present and otherwise consulting RBAC and caching the
// result for ttl. It never returns an error for a cache miss; a cache write failure
// is ignored so the (correct) RBAC result is still returned.
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

// InvalidateUser drops the user's cached permission decisions, forcing the next
// check to re-evaluate RBAC. Call it after changing the user's roles so stale
// allow/deny results are not served. It uses a pattern delete (SCAN + DEL) because
// the cached keys are per (user, resource, permission); a literal DELETE of the
// glob would match nothing.
func (pc *PermissionCache) InvalidateUser(ctx context.Context, userID string) error {
	return pc.cache.DeletePattern(ctx, fmt.Sprintf("perm:%s:*", userID))
}
