package membership

import (
	"context"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	roomRepo *rooms.Repository
	hub      *events.Hub
}

func NewService(roomRepo *rooms.Repository, hub *events.Hub) *Service {
	return &Service{
		roomRepo: roomRepo,
		hub:      hub,
	}
}

func (s *Service) Invite(ctx context.Context, roomID, userID string) error {
	callerID := interceptor.GetUserID(ctx)
	if callerID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	callerUUID, err := uuid.Parse(callerID)
	if err != nil {
		return errors.BadRequest("invalid caller id")
	}

	member, err := s.roomRepo.GetMember(ctx, roomUUID, callerUUID)
	if err != nil {
		return err
	}

	if member.Role != "admin" {
		return errors.Forbidden("only admins can invite users")
	}

	inviteeUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	if err := s.roomRepo.AddMember(ctx, roomUUID, inviteeUUID, "member"); err != nil {
		return err
	}

	s.hub.NotifyRoomJoin(userID, roomID)

	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MemberJoined{
			MemberJoined: &streamv1.MemberJoined{},
		},
	})

	return nil
}

func (s *Service) Remove(ctx context.Context, roomID, userID string) error {
	callerID := interceptor.GetUserID(ctx)
	if callerID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	callerUUID, err := uuid.Parse(callerID)
	if err != nil {
		return errors.BadRequest("invalid caller id")
	}

	member, err := s.roomRepo.GetMember(ctx, roomUUID, callerUUID)
	if err != nil {
		return err
	}

	if member.Role != "admin" {
		return errors.Forbidden("only admins can remove users")
	}

	targetUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	if err := s.roomRepo.RemoveMember(ctx, roomUUID, targetUUID); err != nil {
		return err
	}

	s.hub.NotifyRoomLeave(userID, roomID)

	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MemberRemoved{
			MemberRemoved: &streamv1.MemberRemoved{
				RoomId: roomID,
				UserId: userID,
			},
		},
	})

	return nil
}

func (s *Service) SetRole(ctx context.Context, roomID, userID, role string) error {
	callerID := interceptor.GetUserID(ctx)
	if callerID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	callerUUID, err := uuid.Parse(callerID)
	if err != nil {
		return errors.BadRequest("invalid caller id")
	}

	member, err := s.roomRepo.GetMember(ctx, roomUUID, callerUUID)
	if err != nil {
		return err
	}

	if member.Role != "admin" {
		return errors.Forbidden("only admins can change roles")
	}

	targetUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	return s.roomRepo.UpdateMemberRole(ctx, roomUUID, targetUUID, role)
}

func (s *Service) SetNickname(ctx context.Context, roomID, nickname string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	return s.roomRepo.UpdateMemberNickname(ctx, roomUUID, userUUID, nickname)
}

func (s *Service) GetMember(ctx context.Context, roomID string) (*rooms.Member, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	return s.roomRepo.GetMember(ctx, roomUUID, userUUID)
}

func (s *Service) ListMembers(ctx context.Context, roomID string) ([]*rooms.Member, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	return s.roomRepo.ListMembers(ctx, roomUUID)
}
