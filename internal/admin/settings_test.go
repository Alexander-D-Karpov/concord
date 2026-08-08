package admin

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

// TestRoomSettingsServiceRoundTrip checks the admin service settings surface:
// defaults, an admin update, validation of the who_can_* enums, and that a
// non-admin caller is refused updates.
func TestRoomSettingsServiceRoundTrip(t *testing.T) {
	svc, _, _, room, admin := newAdminTestService(t)
	ctx := context.Background()

	def, err := svc.GetRoomSettings(ctx, admin.String(), room.String())
	if err != nil {
		t.Fatalf("GetRoomSettings: %v", err)
	}
	if def.WhoCanPost != "member" {
		t.Errorf("expected default who_can_post=member, got %q", def.WhoCanPost)
	}

	// Invalid enum is rejected.
	bad := def
	bad.WhoCanInvite = "banana"
	if _, err := svc.UpdateRoomSettings(ctx, admin.String(), room.String(), bad); err == nil {
		t.Error("expected invalid who_can_invite to be rejected")
	}

	// Valid update round-trips and negatives are clamped.
	upd := rooms.RoomSettings{
		SlowModeInterval: 15,
		WhoCanInvite:     "moderator",
		WhoCanPost:       "moderator",
		MemberCap:        -5, // should clamp to 0
		RetentionDays:    30,
		WordFilters:      []string{"nope"},
	}
	got, err := svc.UpdateRoomSettings(ctx, admin.String(), room.String(), upd)
	if err != nil {
		t.Fatalf("UpdateRoomSettings: %v", err)
	}
	if got.WhoCanInvite != "moderator" || got.MemberCap != 0 || got.RetentionDays != 30 {
		t.Errorf("update did not apply/clamp as expected, got %+v", got)
	}

	// Non-admin cannot update.
	pool := testutil.Pool(t)
	stranger := testutil.SeedUser(t, pool, "stranger-"+uuid.NewString()[:8])
	if _, err := svc.UpdateRoomSettings(ctx, stranger.String(), room.String(), upd); err == nil {
		t.Error("expected non-admin update to be refused")
	}
}
