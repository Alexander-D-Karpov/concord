package users

import (
	"context"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	usersv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/users/v1"
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

func (h *Handler) GetUserByHandle(ctx context.Context, req *usersv1.GetUserByHandleRequest) (*commonv1.User, error) {
	if req.Handle == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("handle is required"))
	}

	user, err := h.service.GetUserByHandle(ctx, req.Handle)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoUser(user), nil
}

func (h *Handler) UpdateProfile(ctx context.Context, req *usersv1.UpdateProfileRequest) (*commonv1.User, error) {
	user, err := h.service.UpdateProfile(ctx, req.DisplayName, req.AvatarUrl, req.Bio)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoUser(user), nil
}

func (h *Handler) UpdateStatus(ctx context.Context, req *usersv1.UpdateStatusRequest) (*commonv1.User, error) {
	user, err := h.service.UpdateStatus(ctx, req.Status)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoUser(user), nil
}

func (h *Handler) SearchUsers(ctx context.Context, req *usersv1.SearchUsersRequest) (*usersv1.SearchUsersResponse, error) {
	if req.Query == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("query is required"))
	}

	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}

	var cursor *string
	if req.Cursor != "" {
		cursor = &req.Cursor
	}

	users, nextCursor, err := h.service.SearchUsers(ctx, req.Query, limit, cursor)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoUsers := make([]*commonv1.User, len(users))
	for i, user := range users {
		protoUsers[i] = toProtoUser(user)
	}

	hasMore := nextCursor != nil
	nextCursorStr := ""
	if nextCursor != nil {
		nextCursorStr = *nextCursor
	}

	return &usersv1.SearchUsersResponse{
		Users:      protoUsers,
		NextCursor: nextCursorStr,
		HasMore:    hasMore,
	}, nil
}

func (h *Handler) ListUsersByIDs(ctx context.Context, req *usersv1.ListUsersByIDsRequest) (*usersv1.ListUsersByIDsResponse, error) {
	if len(req.UserIds) == 0 {
		return &usersv1.ListUsersByIDsResponse{Users: []*commonv1.User{}}, nil
	}

	users, err := h.service.ListUsersByIDs(ctx, req.UserIds)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoUsers := make([]*commonv1.User, len(users))
	for i, user := range users {
		protoUsers[i] = toProtoUser(user)
	}

	return &usersv1.ListUsersByIDsResponse{
		Users: protoUsers,
	}, nil
}

func toProtoUser(user *User) *commonv1.User {
	proto := &commonv1.User{
		Id:          user.ID.String(),
		Handle:      user.Handle,
		DisplayName: user.DisplayName,
		AvatarUrl:   user.AvatarURL,
		CreatedAt:   timestamppb.New(user.CreatedAt),
		Status:      user.Status,
	}

	if user.Bio != nil {
		proto.Bio = *user.Bio
	}

	return proto
}
