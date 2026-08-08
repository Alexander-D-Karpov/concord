package rooms

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

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
		t.Skipf("redis unavailable: %v", err)
	}
	return c
}

// TestSettingsCaching verifies that GetSettings is served from cache (a raw DB
// change is not observed while the entry is warm) but that UpdateSettings
// invalidates the cache so subsequent reads reflect the update.
func TestSettingsCaching(t *testing.T) {
	pool := testutil.Pool(t)
	c := newTestCache(t)
	repo := NewRepositoryWithCache(pool, c)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)

	if err := repo.UpdateSettings(ctx, room, RoomSettings{WhoCanInvite: "member", WhoCanPost: "member", MemberCap: 5}); err != nil {
		t.Fatal(err)
	}
	// Warm the cache.
	if s, err := repo.GetSettings(ctx, room); err != nil || s.MemberCap != 5 {
		t.Fatalf("expected member_cap 5, got %+v err=%v", s, err)
	}

	// A raw DB change bypassing UpdateSettings is NOT seen while cached.
	if _, err := pool.Exec(ctx, `UPDATE room_settings SET member_cap = 99 WHERE room_id = $1`, room); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.GetSettings(ctx, room); s.MemberCap != 5 {
		t.Errorf("expected cached member_cap 5 despite raw DB change, got %d", s.MemberCap)
	}

	// UpdateSettings invalidates the cache, so the new value is observed.
	if err := repo.UpdateSettings(ctx, room, RoomSettings{WhoCanInvite: "moderator", WhoCanPost: "member", MemberCap: 7}); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.GetSettings(ctx, room); s.MemberCap != 7 || s.WhoCanInvite != "moderator" {
		t.Errorf("expected invalidated settings member_cap 7 / moderator, got %+v", s)
	}

	_ = c.Delete(ctx, settingsCacheKey(room))
}
