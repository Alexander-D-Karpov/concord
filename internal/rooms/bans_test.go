package rooms

import (
	"context"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

// TestBanLifecycle exercises the ban CRUD in the rooms repository: adding a ban
// makes IsBanned true and surfaces it in ListBans, removing it clears it, and an
// already-expired ban is treated as inactive.
func TestBanLifecycle(t *testing.T) {
	pool := testutil.Pool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	target := testutil.SeedUser(t, pool, "target-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)

	banned, err := repo.IsBanned(ctx, room, target)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if banned {
		t.Fatal("expected not banned initially")
	}

	if err := repo.AddBan(ctx, room, target, owner, nil); err != nil {
		t.Fatalf("AddBan: %v", err)
	}
	banned, err = repo.IsBanned(ctx, room, target)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if !banned {
		t.Fatal("expected banned after AddBan")
	}

	bans, err := repo.ListBans(ctx, room)
	if err != nil {
		t.Fatalf("ListBans: %v", err)
	}
	if len(bans) != 1 || bans[0].UserID != target {
		t.Fatalf("expected one ban for target, got %+v", bans)
	}

	removed, err := repo.RemoveBan(ctx, room, target)
	if err != nil {
		t.Fatalf("RemoveBan: %v", err)
	}
	if !removed {
		t.Fatal("expected RemoveBan to report a ban was removed")
	}
	banned, _ = repo.IsBanned(ctx, room, target)
	if banned {
		t.Fatal("expected not banned after RemoveBan")
	}

	// An already-expired ban is inactive.
	past := time.Now().Add(-time.Hour)
	if err := repo.AddBan(ctx, room, target, owner, &past); err != nil {
		t.Fatalf("AddBan expired: %v", err)
	}
	banned, _ = repo.IsBanned(ctx, room, target)
	if banned {
		t.Fatal("expected expired ban to be inactive")
	}
	active, err := repo.ListBans(ctx, room)
	if err != nil {
		t.Fatalf("ListBans: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("expected no active bans, got %+v", active)
	}
}
