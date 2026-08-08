package authz

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
)

// newTestCache connects to the Redis instance configured via REDIS_HOST/REDIS_PORT
// (defaulting to localhost:6379). It skips the test when Redis is unavailable, so
// the suite stays green in environments without a cache.
func newTestCache(t *testing.T) *cache.Cache {
	t.Helper()
	host := os.Getenv("REDIS_HOST")
	if host == "" {
		host = "localhost"
	}
	port := 6379
	if p := os.Getenv("REDIS_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	c, err := cache.New(host, port, os.Getenv("REDIS_PASSWORD"), 0)
	if err != nil {
		t.Skipf("redis unavailable, skipping: %v", err)
	}
	return c
}

// TestInvalidateUserClearsAllUserDecisions verifies that InvalidateUser removes
// every cached permission decision for a user (across resources) while leaving
// other users' cached decisions intact.
func TestInvalidateUserClearsAllUserDecisions(t *testing.T) {
	c := newTestCache(t)
	pc := NewPermissionCache(c, NewRBAC(), time.Minute)
	ctx := context.Background()

	userA := "user-" + uuid.NewString()
	userB := "user-" + uuid.NewString()
	roomA1 := "room-" + uuid.NewString()
	roomA2 := "room-" + uuid.NewString()
	roomB1 := "room-" + uuid.NewString()

	// Populate cached decisions for userA (two resources) and userB (one).
	if _, err := pc.HasPermission(ctx, userA, roomA1, PermissionReadRoom); err != nil {
		t.Fatalf("HasPermission: %v", err)
	}
	if _, err := pc.HasPermission(ctx, userA, roomA2, PermissionReadRoom); err != nil {
		t.Fatalf("HasPermission: %v", err)
	}
	if _, err := pc.HasPermission(ctx, userB, roomB1, PermissionReadRoom); err != nil {
		t.Fatalf("HasPermission: %v", err)
	}

	if err := pc.InvalidateUser(ctx, userA); err != nil {
		t.Fatalf("InvalidateUser: %v", err)
	}

	// userA's cached decisions must all be gone.
	for _, res := range []string{roomA1, roomA2} {
		key := "perm:" + userA + ":" + res + ":" + string(PermissionReadRoom)
		var v bool
		err := c.Get(ctx, key, &v)
		if !errors.Is(err, cache.ErrCacheMiss) {
			t.Errorf("expected userA key %q to be evicted, got err=%v", key, err)
		}
	}

	// userB's cached decision must survive.
	keyB := "perm:" + userB + ":" + roomB1 + ":" + string(PermissionReadRoom)
	var vb bool
	if err := c.Get(ctx, keyB, &vb); err != nil {
		t.Errorf("expected userB key %q to survive invalidation, got err=%v", keyB, err)
	}

	// cleanup
	_ = c.DeletePattern(ctx, "perm:"+userB+":*")
}
