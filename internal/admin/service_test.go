package admin

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/audit"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func newAdminTestService(t *testing.T) (*Service, *rooms.Repository, *audit.Logger, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool := testutil.Pool(t)
	roomsRepo := rooms.NewRepository(pool)
	hub := events.NewHub(zap.NewNop(), pool, nil)
	auditLogger := audit.NewLogger(pool, zap.NewNop())
	svc := NewService(pool, roomsRepo, hub, zap.NewNop(), auditLogger)

	admin := testutil.SeedUser(t, pool, "admin-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, admin, 0)
	testutil.SeedMembership(t, pool, room, admin, "admin")
	return svc, roomsRepo, auditLogger, room, admin
}

// TestBanUnbanAndAudit checks the full ban lifecycle through the admin service:
// banning marks the user banned and writes an audit record; unbanning clears the
// ban and writes another; ListBans reflects the current state.
func TestBanUnbanAndAudit(t *testing.T) {
	svc, roomsRepo, auditLogger, room, admin := newAdminTestService(t)
	ctx := context.Background()
	target := testutil.SeedUser(t, testutil.Pool(t), "banme-"+uuid.NewString()[:8])

	if err := svc.BanUser(ctx, admin.String(), room.String(), target.String(), 0); err != nil {
		t.Fatalf("BanUser: %v", err)
	}
	if banned, _ := roomsRepo.IsBanned(ctx, room, target); !banned {
		t.Fatal("expected user to be banned")
	}

	bans, err := svc.ListBans(ctx, admin.String(), room.String())
	if err != nil {
		t.Fatalf("ListBans: %v", err)
	}
	if len(bans) != 1 || bans[0].UserID != target {
		t.Fatalf("expected one ban for target, got %+v", bans)
	}

	if err := svc.Unban(ctx, admin.String(), room.String(), target.String()); err != nil {
		t.Fatalf("Unban: %v", err)
	}
	if banned, _ := roomsRepo.IsBanned(ctx, room, target); banned {
		t.Fatal("expected user to be unbanned")
	}

	// Both the ban and the unban should be in the audit log for this room.
	events, err := auditLogger.List(ctx, room.String(), 50, 0)
	if err != nil {
		t.Fatalf("audit List: %v", err)
	}
	var sawBan, sawUnban bool
	for _, e := range events {
		switch e.Action {
		case "user.ban":
			sawBan = true
		case "user.unban":
			sawUnban = true
		}
	}
	if !sawBan || !sawUnban {
		t.Errorf("expected ban and unban audit records, got sawBan=%v sawUnban=%v", sawBan, sawUnban)
	}
}

// TestModerationRequiresPermission verifies a non-admin caller is refused.
func TestModerationRequiresPermission(t *testing.T) {
	svc, _, _, room, _ := newAdminTestService(t)
	ctx := context.Background()
	pool := testutil.Pool(t)
	stranger := testutil.SeedUser(t, pool, "stranger-"+uuid.NewString()[:8])
	target := testutil.SeedUser(t, pool, "victim-"+uuid.NewString()[:8])

	if err := svc.BanUser(ctx, stranger.String(), room.String(), target.String(), 0); err == nil {
		t.Fatal("expected non-admin ban to be refused")
	}
	if err := svc.Unban(ctx, stranger.String(), room.String(), target.String()); err == nil {
		t.Fatal("expected non-admin unban to be refused")
	}
}
