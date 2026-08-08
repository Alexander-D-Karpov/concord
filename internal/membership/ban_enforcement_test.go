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

// TestAcceptRoomInviteRejectsBannedUser proves ban enforcement: a banned user
// cannot accept a room invite to rejoin, but once unbanned the same invite works.
func TestAcceptRoomInviteRejectsBannedUser(t *testing.T) {
	pool := testutil.Pool(t)
	roomRepo := rooms.NewRepository(pool)
	svc := NewService(roomRepo, nil, nil)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	target := testutil.SeedUser(t, pool, "target-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)

	invite, err := roomRepo.CreateRoomInvite(ctx, room, target, owner)
	if err != nil {
		t.Fatalf("CreateRoomInvite: %v", err)
	}

	// Ban the target, then try to accept the invite as the target.
	if err := roomRepo.AddBan(ctx, room, target, owner, nil); err != nil {
		t.Fatalf("AddBan: %v", err)
	}
	authCtx := interceptor.ContextWithAuth(ctx, target.String(), "target", nil)

	if _, err := svc.AcceptRoomInvite(authCtx, invite.ID.String()); err == nil {
		t.Fatal("expected banned user to be refused acceptance, but it succeeded")
	} else if !strings.Contains(strings.ToLower(err.Error()), "ban") {
		t.Errorf("expected a ban-related error, got %v", err)
	}

	// Lift the ban; the same invite should now be acceptable.
	if _, err := roomRepo.RemoveBan(ctx, room, target); err != nil {
		t.Fatalf("RemoveBan: %v", err)
	}
	member, err := svc.AcceptRoomInvite(authCtx, invite.ID.String())
	if err != nil {
		t.Fatalf("expected accept to succeed after unban, got %v", err)
	}
	if member == nil {
		t.Fatal("expected a member back after successful accept")
	}
}
