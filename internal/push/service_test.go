package push

import (
	"context"
	"testing"

	pushv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/push/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

func TestRegisterAndUnregisterDevice(t *testing.T) {
	pool := testutil.Pool(t)
	h := NewHandler(NewRepository(pool))
	user := testutil.SeedUser(t, pool, "ph-"+uuid.NewString()[:8])
	ctx := interceptor.ContextWithAuth(context.Background(), user.String(), "h", nil)

	if _, err := h.RegisterDevice(ctx, &pushv1.RegisterDeviceRequest{
		DeviceId: "d1", Platform: pushv1.DevicePlatform_DEVICE_PLATFORM_ANDROID, FcmToken: "t1",
	}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	list, _ := NewRepository(pool).ListByUser(ctx, user)
	if len(list) != 1 || list[0].FCMToken != "t1" {
		t.Fatalf("expected one device, got %+v", list)
	}
	if _, err := h.UnregisterDevice(ctx, &pushv1.UnregisterDeviceRequest{DeviceId: "d1"}); err != nil {
		t.Fatalf("UnregisterDevice: %v", err)
	}
	list, _ = NewRepository(pool).ListByUser(ctx, user)
	if len(list) != 0 {
		t.Fatalf("expected device removed, got %+v", list)
	}
	if _, err := h.RegisterDevice(context.Background(), &pushv1.RegisterDeviceRequest{DeviceId: "d1", FcmToken: "t1"}); err == nil {
		t.Error("expected unauthenticated RegisterDevice to fail")
	}
}
