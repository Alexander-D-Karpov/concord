package rooms

import (
	"context"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateRoom(ctx context.Context, name string, voiceServerID *string, region *string) (*Room, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	creatorID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	room := &Room{
		Name:      name,
		CreatedBy: creatorID,
		Region:    region,
	}

	if voiceServerID != nil && *voiceServerID != "" {
		serverID, err := uuid.Parse(*voiceServerID)
		if err != nil {
			return nil, errors.BadRequest("invalid voice server id")
		}
		room.VoiceServerID = &serverID
	}

	if err := s.repo.Create(ctx, room); err != nil {
		return nil, err
	}

	if err := s.repo.AddMember(ctx, room.ID, creatorID, "admin"); err != nil {
		return nil, err
	}

	return room, nil
}

func (s *Service) GetRoom(ctx context.Context, roomID string) (*Room, error) {
	id, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	return s.repo.GetByID(ctx, id)
}

func (s *Service) ListRoomsForUser(ctx context.Context) ([]*Room, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	return s.repo.ListForUser(ctx, id)
}

func (s *Service) AttachVoiceServer(ctx context.Context, roomID, serverID string) (*Room, error) {
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

	member, err := s.repo.GetMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, err
	}

	if member.Role != "admin" {
		return nil, errors.Forbidden("only admins can attach voice servers")
	}

	serverUUID, err := uuid.Parse(serverID)
	if err != nil {
		return nil, errors.BadRequest("invalid server id")
	}

	if err := s.repo.UpdateVoiceServer(ctx, roomUUID, serverUUID); err != nil {
		return nil, err
	}

	return s.repo.GetByID(ctx, roomUUID)
}
