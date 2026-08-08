package push

import (
	"context"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/features"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

func TestMuteCheckerRoom(t *testing.T) {
	pool := testutil.Pool(t)
	fr := features.NewRepository(pool)
	mc := NewMuteChecker(fr)
	ctx := context.Background()

	user := testutil.SeedUser(t, pool, "mute-"+uuid.NewString()[:8])
	owner := testutil.SeedUser(t, pool, "own-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)

	// Not muted initially.
	if muted, err := mc.IsMuted(ctx, user, &room, nil); err != nil || muted {
		t.Fatalf("expected not muted, got muted=%v err=%v", muted, err)
	}
	// Mute the room until the far future.
	future := time.Now().Add(time.Hour)
	if err := fr.UpsertNotificationOverride(ctx, user, &room, nil, "nothing", &future, false); err != nil {
		t.Fatalf("UpsertNotificationOverride: %v", err)
	}
	if muted, err := mc.IsMuted(ctx, user, &room, nil); err != nil || !muted {
		t.Fatalf("expected muted after override, got muted=%v err=%v", muted, err)
	}
}
