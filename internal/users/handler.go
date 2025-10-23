package users

import (
	"context"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	usersv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/user
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	usersv1.UnimplementedUsersServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

func (h *Handler) GetSelf(ctx context.Context, req *usersv1.GetSelfRequest) (*commonv1.User, error) {
	user, err := h.service.GetSelf(ctx)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoUser(user), nil
}

func (h *Handler) GetUser(ctx context.Context, req *usersv1.GetUserRequest) (*commonv1.User, error) {
	if req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("user_id is required"))
	}

	user, err := h.service.GetUser(ctx, req.UserId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoUser(user), nil
}

func (h *Handler) UpdateProfile(ctx context.Context, req *usersv1.UpdateProfileRequest) (*commonv1.User, error) {
	user, err := h.service.UpdateProfile(ctx, req.DisplayName, req.AvatarUrl)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoUser(user), nil
}

func toProtoUser(user *User) *commonv1.User {
	return &commonv1.User{
		Id:          user.ID.String(),
		Handle:      user.Handle,
		DisplayName: user.DisplayName,
		AvatarUrl:   user.AvatarURL,
		CreatedAt:   timestamppb.New(user.CreatedAt),
	}
}
