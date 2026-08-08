package membership

import (
	"context"
	"strings"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

// TestCreateInviteWhoCanInvite verifies that when who_can_invite is "moderator", a
// plain member cannot invite but a moderator/admin can.
func TestCreateInviteWhoCanInvite(t *testing.T) {
	pool := testutil.Pool(t)
	roomRepo := rooms.NewRepository(pool)
	svc := NewService(roomRepo, nil, nil)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	member := testutil.SeedUser(t, pool, "member-"+uuid.NewString()[:8])
	invitee1 := testutil.SeedUser(t, pool, "inv1-"+uuid.NewString()[:8])
	invitee2 := testutil.SeedUser(t, pool, "inv2-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)
	testutil.SeedMembership(t, pool, room, owner, "admin")
	testutil.SeedMembership(t, pool, room, member, "member")

	s, _ := roomRepo.GetSettings(ctx, room)
	s.WhoCanInvite = "moderator"
	if err := roomRepo.UpdateSettings(ctx, room, s); err != nil {
		t.Fatal(err)
	}

	// Plain member cannot invite.
	memberCtx := interceptor.ContextWithAuth(ctx, member.String(), "member", nil)
	if _, err := svc.CreateRoomInvite(memberCtx, room.String(), invitee1.String()); err == nil {
		t.Fatal("expected member to be refused when who_can_invite=moderator")
	} else if !strings.Contains(strings.ToLower(err.Error()), "moderator") {
		t.Errorf("expected moderator-related error, got %v", err)
	}

	// Admin can invite.
	adminCtx := interceptor.ContextWithAuth(ctx, owner.String(), "owner", nil)
	if _, err := svc.CreateRoomInvite(adminCtx, room.String(), invitee2.String()); err != nil {
		t.Fatalf("expected admin to be allowed to invite, got %v", err)
	}
}
