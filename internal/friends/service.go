package friends

import (
	"context"
	"time"

	friendsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/friends/v1"
	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	repo      *Repository
	hub       *events.Hub
	usersRepo *users.Repository
}

func NewService(repo *Repository, hub *events.Hub, usersRepo *users.Repository) *Service {
	return &Service{
		repo:      repo,
		hub:       hub,
		usersRepo: usersRepo,
	}
}

func (s *Service) SendFriendRequest(ctx context.Context, toUserID string) (*FriendRequest, *users.User, *users.User, error) {
	fromUserID := interceptor.GetUserID(ctx)
	if fromUserID == "" {
		return nil, nil, nil, errors.Unauthorized("user not authenticated")
	}
	fromUUID, err := uuid.Parse(fromUserID)
	if err != nil {
		return nil, nil, nil, errors.BadRequest("invalid user id")
	}
	toUUID, err := uuid.Parse(toUserID)
	if err != nil {
		return nil, nil, nil, errors.BadRequest("invalid target user id")
	}
	if fromUUID == toUUID {
		return nil, nil, nil, errors.BadRequest("cannot send friend request to yourself")
	}

	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	alreadyFriends, err := s.repo.AreFriends(dbCtx, fromUUID, toUUID)
	if err != nil {
		return nil, nil, nil, errors.Internal("failed to check friendship status", err)
	}
	if alreadyFriends {
		return nil, nil, nil, errors.Conflict("already friends with this user")
	}

	existingRequest, err := s.repo.GetFriendRequestBetweenUsers(dbCtx, fromUUID, toUUID)
	if err != nil && err.Error() != "user not found" {
		return nil, nil, nil, errors.Internal("failed to check existing requests", err)
	}
	if existingRequest != nil && existingRequest.Status == "pending" {
		if existingRequest.FromUserID == fromUUID {
			return nil, nil, nil, errors.Conflict("friend request already sent")
		}
		return nil, nil, nil, errors.Conflict("this user has already sent you a friend request")
	}

	blocked, err := s.repo.IsBlocked(dbCtx, toUUID, fromUUID)
	if err != nil {
		return nil, nil, nil, errors.Internal("failed to check block status", err)
	}
	if blocked {
		return nil, nil, nil, errors.Forbidden("cannot send friend request to this user")
	}

	request, err := s.repo.CreateFriendRequest(dbCtx, fromUUID, toUUID)
	if err != nil {
		return nil, nil, nil, errors.Internal("failed to create friend request", err)
	}

	fromUser, err := s.usersRepo.GetByID(dbCtx, fromUUID)
	if err != nil {
		return nil, nil, nil, errors.Internal("failed to get from user", err)
	}

	toUser, err := s.usersRepo.GetByID(dbCtx, toUUID)
	if err != nil {
		return nil, nil, nil, errors.Internal("failed to get to user", err)
	}

	if s.hub != nil {
		s.hub.BroadcastToUser(toUserID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_FriendRequestCreated{
				FriendRequestCreated: &streamv1.FriendRequestCreated{
					Request: &friendsv1.FriendRequest{
						Id:              request.ID.String(),
						FromUserId:      fromUserID,
						ToUserId:        toUserID,
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
				},
			},
		})
	}

	s.hub.BroadcastToUser(fromUserID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_FriendRequestCreated{
			FriendRequestCreated: &streamv1.FriendRequestCreated{
				Request: &friendsv1.FriendRequest{
					Id:              request.ID.String(),
					FromUserId:      fromUserID,
					ToUserId:        toUserID,
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
			},
		},
	})

	return request, fromUser, toUser, nil
}

func (s *Service) AcceptFriendRequest(ctx context.Context, requestID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}
	reqUUID, err := uuid.Parse(requestID)
	if err != nil {
		return errors.BadRequest("invalid request id")
	}

	req, err := s.repo.GetFriendRequest(ctx, reqUUID)
	if err != nil {
		return err
	}
	if req.ToUserID != userUUID {
		return errors.Forbidden("not authorized to accept this request")
	}
	if req.Status != "pending" {
		return errors.Conflict("request already processed")
	}

	if err := s.repo.CreateFriendship(ctx, req.FromUserID, req.ToUserID); err != nil {
		return err
	}
	if err := s.repo.UpdateRequestStatus(ctx, reqUUID, "accepted"); err != nil {
		return err
	}

	if s.hub != nil {
		fromUser, _ := s.usersRepo.GetByID(ctx, req.FromUserID)
		toUser, _ := s.usersRepo.GetByID(ctx, req.ToUserID)

		protoRequest := &friendsv1.FriendRequest{
			Id:         req.ID.String(),
			FromUserId: req.FromUserID.String(),
			ToUserId:   req.ToUserID.String(),
			Status:     friendsv1.FriendRequestStatus_FRIEND_REQUEST_STATUS_ACCEPTED,
			CreatedAt:  timestamppb.New(req.CreatedAt),
			UpdatedAt:  timestamppb.Now(),
		}

		if fromUser != nil {
			protoRequest.FromHandle = fromUser.Handle
			protoRequest.FromDisplayName = fromUser.DisplayName
			protoRequest.FromAvatarUrl = fromUser.AvatarURL
		}
		if toUser != nil {
			protoRequest.ToHandle = toUser.Handle
			protoRequest.ToDisplayName = toUser.DisplayName
			protoRequest.ToAvatarUrl = toUser.AvatarURL
		}

		ev := &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_FriendRequestUpdated{
				FriendRequestUpdated: &streamv1.FriendRequestUpdated{
					Request: protoRequest,
				},
			},
		}
		s.hub.BroadcastToUser(req.FromUserID.String(), ev)
		s.hub.BroadcastToUser(req.ToUserID.String(), ev)
	}
	return nil
}

func (s *Service) RejectFriendRequest(ctx context.Context, requestID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}
	reqUUID, err := uuid.Parse(requestID)
	if err != nil {
		return errors.BadRequest("invalid request id")
	}
	req, err := s.repo.GetFriendRequest(ctx, reqUUID)
	if err != nil {
		return err
	}
	if req.ToUserID != userUUID {
		return errors.Forbidden("not authorized to reject this request")
	}
	if req.Status != "pending" {
		return errors.Conflict("request already processed")
	}

	if err := s.repo.UpdateRequestStatus(ctx, reqUUID, "rejected"); err != nil {
		return err
	}

	if s.hub != nil {
		fromUser, _ := s.usersRepo.GetByID(ctx, req.FromUserID)
		toUser, _ := s.usersRepo.GetByID(ctx, req.ToUserID)

		protoRequest := &friendsv1.FriendRequest{
			Id:         req.ID.String(),
			FromUserId: req.FromUserID.String(),
			ToUserId:   req.ToUserID.String(),
			Status:     friendsv1.FriendRequestStatus_FRIEND_REQUEST_STATUS_REJECTED,
			CreatedAt:  timestamppb.New(req.CreatedAt),
			UpdatedAt:  timestamppb.Now(),
		}

		if fromUser != nil {
			protoRequest.FromHandle = fromUser.Handle
			protoRequest.FromDisplayName = fromUser.DisplayName
			protoRequest.FromAvatarUrl = fromUser.AvatarURL
		}
		if toUser != nil {
			protoRequest.ToHandle = toUser.Handle
			protoRequest.ToDisplayName = toUser.DisplayName
			protoRequest.ToAvatarUrl = toUser.AvatarURL
		}

		ev := &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_FriendRequestUpdated{
				FriendRequestUpdated: &streamv1.FriendRequestUpdated{
					Request: protoRequest,
				},
			},
		}
		s.hub.BroadcastToUser(req.FromUserID.String(), ev)
		s.hub.BroadcastToUser(req.ToUserID.String(), ev)
	}
	return nil
}

func (s *Service) CancelFriendRequest(ctx context.Context, requestID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}
	reqUUID, err := uuid.Parse(requestID)
	if err != nil {
		return errors.BadRequest("invalid request id")
	}

	req, err := s.repo.GetFriendRequest(ctx, reqUUID)
	if err != nil {
		return err
	}
	if req.FromUserID != userUUID {
		return errors.Forbidden("not authorized to cancel this request")
	}
	if req.Status != "pending" {
		return errors.Conflict("request already processed")
	}

	if err := s.repo.UpdateRequestStatus(ctx, reqUUID, "rejected"); err != nil {
		return err
	}

	if s.hub != nil {
		fromUser, _ := s.usersRepo.GetByID(ctx, req.FromUserID)
		toUser, _ := s.usersRepo.GetByID(ctx, req.ToUserID)

		protoRequest := &friendsv1.FriendRequest{
			Id:         req.ID.String(),
			FromUserId: req.FromUserID.String(),
			ToUserId:   req.ToUserID.String(),
			Status:     friendsv1.FriendRequestStatus_FRIEND_REQUEST_STATUS_REJECTED,
			CreatedAt:  timestamppb.New(req.CreatedAt),
			UpdatedAt:  timestamppb.Now(),
		}

		if fromUser != nil {
			protoRequest.FromHandle = fromUser.Handle
			protoRequest.FromDisplayName = fromUser.DisplayName
			protoRequest.FromAvatarUrl = fromUser.AvatarURL
		}
		if toUser != nil {
			protoRequest.ToHandle = toUser.Handle
			protoRequest.ToDisplayName = toUser.DisplayName
			protoRequest.ToAvatarUrl = toUser.AvatarURL
		}

		ev := &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_FriendRequestUpdated{
				FriendRequestUpdated: &streamv1.FriendRequestUpdated{
					Request: protoRequest,
				},
			},
		}
		s.hub.BroadcastToUser(req.FromUserID.String(), ev)
		s.hub.BroadcastToUser(req.ToUserID.String(), ev)
	}
	return nil
}

func (s *Service) RemoveFriend(ctx context.Context, friendUserID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	friendUUID, err := uuid.Parse(friendUserID)
	if err != nil {
		return errors.BadRequest("invalid friend user id")
	}

	if err := s.repo.DeleteFriendship(ctx, userUUID, friendUUID); err != nil {
		return err
	}

	if s.hub != nil {
		s.hub.BroadcastToUser(friendUserID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_FriendRemoved{
				FriendRemoved: &streamv1.FriendRemoved{
					UserId:    friendUserID,
					RemovedBy: userID,
				},
			},
		})

		s.hub.BroadcastToUser(userID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_FriendRemoved{
				FriendRemoved: &streamv1.FriendRemoved{
					UserId:    userID,
					RemovedBy: userID,
				},
			},
		})
	}

	return nil
}

func (s *Service) ListFriends(ctx context.Context) ([]*Friend, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	return s.repo.ListFriends(ctx, userUUID)
}

func (s *Service) ListPendingRequests(ctx context.Context) (incoming, outgoing []*FriendRequestWithUser, err error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, nil, errors.BadRequest("invalid user id")
	}

	incoming, err = s.repo.ListIncomingRequestsWithUsers(ctx, userUUID)
	if err != nil {
		return nil, nil, err
	}

	outgoing, err = s.repo.ListOutgoingRequestsWithUsers(ctx, userUUID)
	if err != nil {
		return nil, nil, err
	}

	return incoming, outgoing, nil
}

func (s *Service) BlockUser(ctx context.Context, blockedUserID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	blockedUUID, err := uuid.Parse(blockedUserID)
	if err != nil {
		return errors.BadRequest("invalid blocked user id")
	}

	if userUUID == blockedUUID {
		return errors.BadRequest("cannot block yourself")
	}

	_ = s.repo.DeleteFriendship(ctx, userUUID, blockedUUID)

	if err := s.repo.BlockUser(ctx, userUUID, blockedUUID); err != nil {
		return err
	}

	if s.hub != nil {
		s.hub.BroadcastToUser(blockedUserID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_UserBlocked{
				UserBlocked: &streamv1.UserBlocked{
					BlockerId: userID,
				},
			},
		})
	}

	return nil
}

func (s *Service) UnblockUser(ctx context.Context, blockedUserID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	blockedUUID, err := uuid.Parse(blockedUserID)
	if err != nil {
		return errors.BadRequest("invalid blocked user id")
	}

	if err := s.repo.UnblockUser(ctx, userUUID, blockedUUID); err != nil {
		return err
	}

	if s.hub != nil {
		s.hub.BroadcastToUser(blockedUserID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_UserUnblocked{
				UserUnblocked: &streamv1.UserUnblocked{
					UnblockerId: userID,
				},
			},
		})
	}

	return nil
}

func (s *Service) ListBlockedUsers(ctx context.Context) ([]string, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	blockedIDs, err := s.repo.ListBlockedUsers(ctx, userUUID)
	if err != nil {
		return nil, err
	}

	result := make([]string, len(blockedIDs))
	for i, id := range blockedIDs {
		result[i] = id.String()
	}

	return result, nil
}
