package admin

import (
	"context"
	"testing"

	adminv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/admin/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

// TestListBansHandlerWirePath drives a ban through the gRPC handler and asserts the
// proto response mapping (entry present, ExpiresAt == 0 for a permanent ban). This
// covers the handler translation that the service-level tests do not.
func TestListBansHandlerWirePath(t *testing.T) {
	svc, _, _, room, admin := newAdminTestService(t)
	h := NewHandler(svc)
	target := testutil.SeedUser(t, testutil.Pool(t), "banme-"+uuid.NewString()[:8])
	adminCtx := interceptor.ContextWithAuth(context.Background(), admin.String(), "admin", nil)

	// Permanent ban (duration 0) via the handler.
	if _, err := h.Ban(adminCtx, &adminv1.BanRequest{RoomId: room.String(), UserId: target.String(), DurationSeconds: 0}); err != nil {
		t.Fatalf("Ban handler: %v", err)
	}

	resp, err := h.ListBans(adminCtx, &adminv1.ListBansRequest{RoomId: room.String()})
	if err != nil {
		t.Fatalf("ListBans handler: %v", err)
	}
	if len(resp.Bans) != 1 {
		t.Fatalf("expected 1 ban entry, got %d", len(resp.Bans))
	}
	entry := resp.Bans[0]
	if entry.UserId != target.String() {
		t.Errorf("expected banned user %s, got %s", target.String(), entry.UserId)
	}
	if entry.BannedBy != admin.String() {
		t.Errorf("expected banned_by %s, got %s", admin.String(), entry.BannedBy)
	}
	if entry.ExpiresAt != 0 {
		t.Errorf("expected ExpiresAt=0 for a permanent ban, got %d", entry.ExpiresAt)
	}
	if entry.CreatedAt == 0 {
		t.Error("expected CreatedAt to be set")
	}

	// A non-admin caller must be refused at the handler boundary too.
	stranger := testutil.SeedUser(t, testutil.Pool(t), "stranger-"+uuid.NewString()[:8])
	strangerCtx := interceptor.ContextWithAuth(context.Background(), stranger.String(), "stranger", nil)
	if _, err := h.ListBans(strangerCtx, &adminv1.ListBansRequest{RoomId: room.String()}); err == nil {
		t.Fatal("expected non-admin ListBans to be refused")
	}
}

// TestRoomSettingsHandlerWirePath drives Update/GetRoomSettings through the gRPC
// handler and asserts the proto mapping round-trips.
func TestRoomSettingsHandlerWirePath(t *testing.T) {
	svc, _, _, room, admin := newAdminTestService(t)
	h := NewHandler(svc)
	adminCtx := interceptor.ContextWithAuth(context.Background(), admin.String(), "admin", nil)

	updResp, err := h.UpdateRoomSettings(adminCtx, &adminv1.UpdateRoomSettingsRequest{
		RoomId: room.String(),
		Settings: &adminv1.RoomSettings{
			SlowModeInterval: 20,
			WhoCanInvite:     "moderator",
			WhoCanPost:       "member",
			IsPrivate:        true,
			MemberCap:        42,
			RetentionDays:    14,
			GifsEnabled:      false,
			WordFilters:      []string{"foo", "bar"},
		},
	})
	if err != nil {
		t.Fatalf("UpdateRoomSettings handler: %v", err)
	}
	s := updResp.Settings
	if s.SlowModeInterval != 20 || s.WhoCanInvite != "moderator" || !s.IsPrivate ||
		s.MemberCap != 42 || s.RetentionDays != 14 || s.GifsEnabled || len(s.WordFilters) != 2 {
		t.Errorf("update response did not round-trip: %+v", s)
	}

	getResp, err := h.GetRoomSettings(adminCtx, &adminv1.GetRoomSettingsRequest{RoomId: room.String()})
	if err != nil {
		t.Fatalf("GetRoomSettings handler: %v", err)
	}
	if getResp.Settings.MemberCap != 42 || getResp.Settings.WhoCanInvite != "moderator" {
		t.Errorf("get did not reflect update: %+v", getResp.Settings)
	}

	// Invalid enum rejected at the boundary.
	if _, err := h.UpdateRoomSettings(adminCtx, &adminv1.UpdateRoomSettingsRequest{
		RoomId:   room.String(),
		Settings: &adminv1.RoomSettings{WhoCanInvite: "nonsense", WhoCanPost: "member"},
	}); err == nil {
		t.Error("expected invalid who_can_invite to be rejected")
	}
}
