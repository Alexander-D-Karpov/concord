package friends

import (
	"context"

	friendsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/friends/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	friendsv1.UnimplementedFriendsServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) SendFriendRequest(ctx context.Context, req *friendsv1.SendFriendRequestRequest) (*friendsv1.SendFriendRequestResponse, error) {
	if req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("user_id is required"))
	}

	request, fromUser, toUser, err := h.service.SendFriendRequest(ctx, req.UserId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &friendsv1.SendFriendRequestResponse{
		Request: &friendsv1.FriendRequest{
			Id:              request.ID.String(),
			FromUserId:      request.FromUserID.String(),
			ToUserId:        request.ToUserID.String(),
			Status:          friendsv1.FriendRequestStatus_FRIEND_REQUEST_STATUS_PENDING,
			CreatedAt:       timestamppb.New(request.CreatedAt),
			UpdatedAt:       timestamppb.New(request.UpdatedAt),
			FromHandle:      fromUser.Handle,
			FromDisplayName: fromUser.DisplayName,
			FromAvatarUrl:   fromUser.AvatarURL,
			ToHandle:        toUser.Handle,
			ToDisplayName:   toUser.DisplayName,
			ToAvatarUrl:     toUser.AvatarURL,
		},
	}, nil
}

func (h *Handler) AcceptFriendRequest(ctx context.Context, req *friendsv1.AcceptFriendRequestRequest) (*friendsv1.EmptyResponse, error) {
	if req.RequestId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("request_id is required"))
	}

	if err := h.service.AcceptFriendRequest(ctx, req.RequestId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &friendsv1.EmptyResponse{}, nil
}

func (h *Handler) RejectFriendRequest(ctx context.Context, req *friendsv1.RejectFriendRequestRequest) (*friendsv1.EmptyResponse, error) {
	if req.RequestId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("request_id is required"))
	}

	if err := h.service.RejectFriendRequest(ctx, req.RequestId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &friendsv1.EmptyResponse{}, nil
}

func (h *Handler) CancelFriendRequest(ctx context.Context, req *friendsv1.CancelFriendRequestRequest) (*friendsv1.EmptyResponse, error) {
	if req.RequestId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("request_id is required"))
	}

	if err := h.service.CancelFriendRequest(ctx, req.RequestId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &friendsv1.EmptyResponse{}, nil
}

func (h *Handler) RemoveFriend(ctx context.Context, req *friendsv1.RemoveFriendRequest) (*friendsv1.EmptyResponse, error) {
	if req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("user_id is required"))
	}

	if err := h.service.RemoveFriend(ctx, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &friendsv1.EmptyResponse{}, nil
}

func (h *Handler) ListFriends(ctx context.Context, req *friendsv1.ListFriendsRequest) (*friendsv1.ListFriendsResponse, error) {
	friends, err := h.service.ListFriends(ctx)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoFriends := make([]*friendsv1.Friend, len(friends))
	for i, friend := range friends {
		protoFriends[i] = &friendsv1.Friend{
			UserId:       friend.UserID.String(),
			Handle:       friend.Handle,
			DisplayName:  friend.DisplayName,
			AvatarUrl:    friend.AvatarURL,
			Status:       friend.Status,
			FriendsSince: timestamppb.New(friend.FriendsSince),
		}
	}

	return &friendsv1.ListFriendsResponse{
		Friends: protoFriends,
	}, nil
}

func (h *Handler) ListPendingRequests(ctx context.Context, req *friendsv1.ListPendingRequestsRequest) (*friendsv1.ListPendingRequestsResponse, error) {
	incoming, outgoing, err := h.service.ListPendingRequests(ctx)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoIncoming := make([]*friendsv1.FriendRequest, len(incoming))
	for i, r := range incoming {
		protoIncoming[i] = toProtoFriendRequestWithUser(r)
	}

	protoOutgoing := make([]*friendsv1.FriendRequest, len(outgoing))
	for i, r := range outgoing {
		protoOutgoing[i] = toProtoFriendRequestWithUser(r)
	}

	return &friendsv1.ListPendingRequestsResponse{
		Incoming: protoIncoming,
		Outgoing: protoOutgoing,
	}, nil
}

func toProtoFriendRequestWithUser(req *FriendRequestWithUser) *friendsv1.FriendRequest {
	status := friendsv1.FriendRequestStatus_FRIEND_REQUEST_STATUS_PENDING
	switch req.Status {
	case "accepted":
		status = friendsv1.FriendRequestStatus_FRIEND_REQUEST_STATUS_ACCEPTED
	case "rejected":
		status = friendsv1.FriendRequestStatus_FRIEND_REQUEST_STATUS_REJECTED
	}

	return &friendsv1.FriendRequest{
		Id:              req.ID.String(),
		FromUserId:      req.FromUserID.String(),
		ToUserId:        req.ToUserID.String(),
		Status:          status,
		CreatedAt:       timestamppb.New(req.CreatedAt),
		UpdatedAt:       timestamppb.New(req.UpdatedAt),
		FromHandle:      req.FromHandle,
		FromDisplayName: req.FromDisplay,
		FromAvatarUrl:   req.FromAvatar,
		ToHandle:        req.ToHandle,
		ToDisplayName:   req.ToDisplay,
		ToAvatarUrl:     req.ToAvatar,
	}
}

func (h *Handler) BlockUser(ctx context.Context, req *friendsv1.BlockUserRequest) (*friendsv1.EmptyResponse, error) {
	if req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("user_id is required"))
	}

	if err := h.service.BlockUser(ctx, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &friendsv1.EmptyResponse{}, nil
}

func (h *Handler) UnblockUser(ctx context.Context, req *friendsv1.UnblockUserRequest) (*friendsv1.EmptyResponse, error) {
	if req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("user_id is required"))
	}

	if err := h.service.UnblockUser(ctx, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &friendsv1.EmptyResponse{}, nil
}

func (h *Handler) ListBlockedUsers(ctx context.Context, req *friendsv1.ListBlockedUsersRequest) (*friendsv1.ListBlockedUsersResponse, error) {
	userIDs, err := h.service.ListBlockedUsers(ctx)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &friendsv1.ListBlockedUsersResponse{
		UserIds: userIDs,
	}, nil
}
