package admin

import (
	"context"

	adminv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/admin/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Handler struct {
	adminv1.UnimplementedAdminServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

func (h *Handler) Kick(ctx context.Context, req *adminv1.KickRequest) (*adminv1.EmptyResponse, error) {
	if req.RoomId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id is required")
	}
	if req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	adminUserID := interceptor.GetUserID(ctx)
	if adminUserID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	if err := h.service.KickUser(ctx, adminUserID, req.RoomId, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &adminv1.EmptyResponse{}, nil
}

func (h *Handler) Ban(ctx context.Context, req *adminv1.BanRequest) (*adminv1.EmptyResponse, error) {
	if req.RoomId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id is required")
	}
	if req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	adminUserID := interceptor.GetUserID(ctx)
	if adminUserID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	if err := h.service.BanUser(ctx, adminUserID, req.RoomId, req.UserId, req.DurationSeconds); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &adminv1.EmptyResponse{}, nil
}

func (h *Handler) Mute(ctx context.Context, req *adminv1.MuteRequest) (*adminv1.EmptyResponse, error) {
	if req.RoomId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id is required")
	}
	if req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	adminUserID := interceptor.GetUserID(ctx)
	if adminUserID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	if err := h.service.MuteUser(ctx, adminUserID, req.RoomId, req.UserId, req.Muted); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &adminv1.EmptyResponse{}, nil
}
