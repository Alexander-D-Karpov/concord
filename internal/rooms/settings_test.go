package rooms

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

// TestRoomSettingsDefaultsAndRoundTrip verifies that a room with no settings row
// reports defaults, and that UpdateSettings persists all fields — including syncing
// is_private/slow_mode_interval onto the rooms table and replacing the word list.
func TestRoomSettingsDefaultsAndRoundTrip(t *testing.T) {
	pool := testutil.Pool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)

	// Defaults for a room with no settings row.
	def, err := repo.GetSettings(ctx, room)
	if err != nil {
		t.Fatalf("GetSettings defaults: %v", err)
	}
	if def.WhoCanInvite != "member" || def.WhoCanPost != "member" {
		t.Errorf("expected default who_can_* = member, got %+v", def)
	}
	if !def.LinkPreviewsEnabled || !def.GifsEnabled || !def.StickersEnabled {
		t.Errorf("expected content toggles default true, got %+v", def)
	}
	if def.MemberCap != 0 || def.RetentionDays != 0 || def.RequireApproval {
		t.Errorf("expected numeric/bool defaults zero/false, got %+v", def)
	}
	if len(def.WordFilters) != 0 {
		t.Errorf("expected no word filters by default, got %v", def.WordFilters)
	}

	// Update all fields.
	want := RoomSettings{
		SlowModeInterval:    30,
		WhoCanInvite:        "moderator",
		WhoCanPost:          "moderator",
		IsPrivate:           true,
		RequireApproval:     true,
		MemberCap:           50,
		RetentionDays:       7,
		LinkPreviewsEnabled: false,
		GifsEnabled:         false,
		StickersEnabled:     false,
		WordFilters:         []string{"badword", "another"},
	}
	if err := repo.UpdateSettings(ctx, room, want); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	got, err := repo.GetSettings(ctx, room)
	if err != nil {
		t.Fatalf("GetSettings after update: %v", err)
	}
	if got.SlowModeInterval != 30 || got.WhoCanInvite != "moderator" || got.WhoCanPost != "moderator" ||
		!got.IsPrivate || !got.RequireApproval || got.MemberCap != 50 || got.RetentionDays != 7 ||
		got.LinkPreviewsEnabled || got.GifsEnabled || got.StickersEnabled {
		t.Errorf("settings did not round-trip, got %+v", got)
	}
	if len(got.WordFilters) != 2 {
		t.Errorf("expected 2 word filters, got %v", got.WordFilters)
	}

	// is_private and slow_mode_interval must be synced onto the rooms table.
	var isPrivate bool
	var slow int
	if err := pool.QueryRow(ctx, `SELECT is_private, slow_mode_interval FROM rooms WHERE id = $1`, room).Scan(&isPrivate, &slow); err != nil {
		t.Fatalf("read rooms: %v", err)
	}
	if !isPrivate || slow != 30 {
		t.Errorf("expected rooms.is_private=true, slow_mode_interval=30, got %v %d", isPrivate, slow)
	}

	// Full-replace semantics: updating with a smaller word list replaces, not merges.
	want.WordFilters = []string{"only"}
	if err := repo.UpdateSettings(ctx, room, want); err != nil {
		t.Fatalf("UpdateSettings replace: %v", err)
	}
	got, _ = repo.GetSettings(ctx, room)
	if len(got.WordFilters) != 1 || got.WordFilters[0] != "only" {
		t.Errorf("expected word filters replaced to [only], got %v", got.WordFilters)
	}
}

// TestCountMembers verifies the member counter used for member-cap enforcement.
func TestCountMembers(t *testing.T) {
	pool := testutil.Pool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)
	testutil.SeedMembership(t, pool, room, owner, "admin")
	u2 := testutil.SeedUser(t, pool, "m2-"+uuid.NewString()[:8])
	testutil.SeedMembership(t, pool, room, u2, "member")

	n, err := repo.CountMembers(ctx, room)
	if err != nil {
		t.Fatalf("CountMembers: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 members, got %d", n)
	}
}
