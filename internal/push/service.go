package push

import (
	"context"

	pushv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/push/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Handler is the gRPC PushService server: device registration for the authenticated caller.
type Handler struct {
	pushv1.UnimplementedPushServiceServer
	repo *Repository
}

// NewHandler returns a PushService handler backed by repo.
func NewHandler(repo *Repository) *Handler { return &Handler{repo: repo} }

func callerID(ctx context.Context) (uuid.UUID, error) {
	id := interceptor.GetUserID(ctx)
	if id == "" {
		return uuid.Nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, status.Error(codes.Internal, "invalid caller id")
	}
	return u, nil
}

// RegisterDevice upserts the caller's device (token rotation is an update).
func (h *Handler) RegisterDevice(ctx context.Context, req *pushv1.RegisterDeviceRequest) (*pushv1.RegisterDeviceResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if req.DeviceId == "" || req.FcmToken == "" {
		return nil, status.Error(codes.InvalidArgument, "device_id and fcm_token are required")
	}
	if err := h.repo.Upsert(ctx, Device{
		UserID: user, DeviceID: req.DeviceId, Platform: "android",
		FCMToken: req.FcmToken, AppVersion: req.AppVersion, Locale: req.Locale,
	}); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("register device", err))
	}
	return &pushv1.RegisterDeviceResponse{DeviceId: req.DeviceId}, nil
}

// UnregisterDevice removes the caller's device.
func (h *Handler) UnregisterDevice(ctx context.Context, req *pushv1.UnregisterDeviceRequest) (*emptypb.Empty, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := h.repo.DeleteByUserDevice(ctx, user, req.DeviceId); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("unregister device", err))
	}
	return &emptypb.Empty{}, nil
}
