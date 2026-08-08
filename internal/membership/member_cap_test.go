package membership

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

// TestAcceptRoomInviteMemberCap verifies that accepting an invite is refused when
// the room is at its member_cap, and allowed once the cap is raised.
func TestAcceptRoomInviteMemberCap(t *testing.T) {
	pool := testutil.Pool(t)
	roomRepo := rooms.NewRepository(pool)
	svc := NewService(roomRepo, nil, nil)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	target := testutil.SeedUser(t, pool, "target-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)
	testutil.SeedMembership(t, pool, room, owner, "admin") // room now has 1 member

	// Cap at 1 (already full).
	s, _ := roomRepo.GetSettings(ctx, room)
	s.MemberCap = 1
	if err := roomRepo.UpdateSettings(ctx, room, s); err != nil {
		t.Fatal(err)
	}

	invite, err := roomRepo.CreateRoomInvite(ctx, room, target, owner)
	if err != nil {
		t.Fatal(err)
	}
	authCtx := interceptor.ContextWithAuth(ctx, target.String(), "target", nil)

	if _, err := svc.AcceptRoomInvite(authCtx, invite.ID.String()); err == nil {
		t.Fatal("expected accept to be refused when room is at member_cap")
	}

	// Raise the cap; accept should now succeed.
	s.MemberCap = 5
	if err := roomRepo.UpdateSettings(ctx, room, s); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptRoomInvite(authCtx, invite.ID.String()); err != nil {
		t.Fatalf("expected accept to succeed after raising cap, got %v", err)
	}
}
