package push

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

func TestPushDeviceRepository(t *testing.T) {
	pool := testutil.Pool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	user := testutil.SeedUser(t, pool, "pushu-"+uuid.NewString()[:8])
	dev := Device{UserID: user, DeviceID: "dev-A", Platform: "android", FCMToken: "tok-1", AppVersion: "1.0", Locale: "en"}

	if err := repo.Upsert(ctx, dev); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	dev.FCMToken = "tok-2"
	if err := repo.Upsert(ctx, dev); err != nil {
		t.Fatalf("Upsert rotate: %v", err)
	}
	list, err := repo.ListByUser(ctx, user)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) != 1 || list[0].FCMToken != "tok-2" {
		t.Fatalf("expected one device with rotated token, got %+v", list)
	}
	if err := repo.DeleteByToken(ctx, "tok-2"); err != nil {
		t.Fatalf("DeleteByToken: %v", err)
	}
	list, _ = repo.ListByUser(ctx, user)
	if len(list) != 0 {
		t.Fatalf("expected token pruned, got %+v", list)
	}
	_ = repo.Upsert(ctx, Device{UserID: user, DeviceID: "dev-A", Platform: "android", FCMToken: "tok-3"})
	removed, err := repo.DeleteByUserDevice(ctx, user, "dev-A")
	if err != nil || !removed {
		t.Fatalf("DeleteByUserDevice: removed=%v err=%v", removed, err)
	}
}
