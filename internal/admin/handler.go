package admin

import (
	"context"

	adminv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/admin/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Handler struct {
	adminv1.UnimplementedAdminServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Kick(ctx context.Context, req *adminv1.KickRequest) (*adminv1.EmptyResponse, error) {
	if req.RoomId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id and user_id are required")
	}

	if err := h.service.Kick(ctx, req.RoomId, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &adminv1.EmptyResponse{}, nil
}

func (h *Handler) Ban(ctx context.Context, req *adminv1.BanRequest) (*adminv1.EmptyResponse, error) {
	if req.RoomId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id and user_id are required")
	}

	if err := h.service.Ban(ctx, req.RoomId, req.UserId, req.DurationSeconds); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &adminv1.EmptyResponse{}, nil
}

func (h *Handler) Mute(ctx context.Context, req *adminv1.MuteRequest) (*adminv1.EmptyResponse, error) {
	if req.RoomId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id and user_id are required")
	}

	if err := h.service.Mute(ctx, req.RoomId, req.UserId, req.Muted); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &adminv1.EmptyResponse{}, nil
}
