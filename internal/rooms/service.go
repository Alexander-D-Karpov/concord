package rooms

import (
	"context"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	repo *Repository
	hub  *events.Hub
}

func NewService(repo *Repository, hub *events.Hub) *Service {
	return &Service{
		repo: repo,
		hub:  hub,
	}
}

func (s *Service) CreateRoom(ctx context.Context, name string, voiceServerID *string, region *string, description string, isPrivate bool) (*Room, error) {
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
		IsPrivate: isPrivate,
	}

	if description != "" {
		room.Description = &description
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

	if s.hub != nil {
		s.hub.NotifyRoomJoin(userID, room.ID.String())
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

func (s *Service) UpdateRoom(ctx context.Context, roomID, name, description string, isPrivate bool) (*Room, error) {
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
		return nil, errors.Forbidden("only admins can update rooms")
	}

	room, err := s.repo.GetByID(ctx, roomUUID)
	if err != nil {
		return nil, err
	}

	if name != "" {
		room.Name = name
	}

	if description != "" {
		room.Description = &description
	}

	room.IsPrivate = isPrivate

	if err := s.repo.Update(ctx, room); err != nil {
		return nil, err
	}

	if s.hub != nil {
		s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_RoomUpdated{
				RoomUpdated: &streamv1.RoomUpdated{},
			},
		})
	}

	return room, nil
}

func (s *Service) DeleteRoom(ctx context.Context, roomID string) error {
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

	room, err := s.repo.GetByID(ctx, roomUUID)
	if err != nil {
		return err
	}

	if room.CreatedBy != userUUID {
		return errors.Forbidden("only room creator can delete the room")
	}

	members, err := s.repo.ListMembers(ctx, roomUUID)
	if err == nil && s.hub != nil {
		for _, member := range members {
			s.hub.NotifyRoomLeave(member.UserID.String(), roomID)
		}
	}

	return s.repo.SoftDelete(ctx, roomUUID)
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
