package membership

import (
	"context"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/google/uuid"
)

type Service struct {
	roomRepo *rooms.Repository
}

func NewService(roomRepo *rooms.Repository) *Service {
	return &Service{roomRepo: roomRepo}
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

	return s.roomRepo.AddMember(ctx, roomUUID, inviteeUUID, "member")
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

	return s.roomRepo.RemoveMember(ctx, roomUUID, targetUUID)
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

func (s *Service) ListMembers(ctx context.Context, roomID string) ([]*rooms.Member, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	return s.roomRepo.ListMembers(ctx, roomUUID)
}
