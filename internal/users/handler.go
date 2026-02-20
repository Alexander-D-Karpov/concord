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
	return &Handler{service: service}
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
	return &usersv1.ListUsersByIDsResponse{Users: protoUsers}, nil
}

func (h *Handler) UploadAvatar(ctx context.Context, req *usersv1.UploadAvatarRequest) (*usersv1.UploadAvatarResponse, error) {
	if len(req.ImageData) == 0 {
		return nil, errors.ToGRPCError(errors.BadRequest("image_data is required"))
	}
	user, av, err := h.service.UploadAvatar(ctx, req.ImageData, req.Filename)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &usersv1.UploadAvatarResponse{
		AvatarUrl:    user.AvatarURL,
		ThumbnailUrl: user.AvatarThumbnailURL,
		Avatar:       toProtoAvatarEntry(av),
	}, nil
}

func (h *Handler) DeleteAvatar(ctx context.Context, req *usersv1.DeleteAvatarRequest) (*usersv1.DeleteAvatarResponse, error) {
	if req.AvatarId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("avatar_id is required"))
	}
	if err := h.service.DeleteAvatar(ctx, req.AvatarId); err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &usersv1.DeleteAvatarResponse{}, nil
}

func (h *Handler) GetAvatarHistory(ctx context.Context, req *usersv1.GetAvatarHistoryRequest) (*usersv1.GetAvatarHistoryResponse, error) {
	if req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("user_id is required"))
	}
	avatars, err := h.service.GetAvatarHistory(ctx, req.UserId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	proto := make([]*commonv1.AvatarEntry, len(avatars))
	for i, av := range avatars {
		proto[i] = toProtoAvatarEntry(av)
	}
	return &usersv1.GetAvatarHistoryResponse{Avatars: proto}, nil
}

func toProtoUser(user *User) *commonv1.User {
	proto := &commonv1.User{
		Id:                 user.ID.String(),
		Handle:             user.Handle,
		DisplayName:        user.DisplayName,
		AvatarUrl:          user.AvatarURL,
		AvatarThumbnailUrl: user.AvatarThumbnailURL,
		CreatedAt:          timestamppb.New(user.CreatedAt),
		Status:             user.Status,
	}
	if user.Bio != "" {
		proto.Bio = user.Bio
	}
	return proto
}

func toProtoAvatarEntry(av *UserAvatar) *commonv1.AvatarEntry {
	return &commonv1.AvatarEntry{
		Id:               av.ID.String(),
		UserId:           av.UserID.String(),
		FullUrl:          av.FullURL,
		ThumbnailUrl:     av.ThumbnailURL,
		OriginalFilename: av.OriginalFilename,
		CreatedAt:        timestamppb.New(av.CreatedAt),
	}
}
