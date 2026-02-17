package typing

import (
	"context"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
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

func (s *Service) StartTypingInRoom(ctx context.Context, userID, roomID uuid.UUID) error {
	if err := s.repo.SetTypingInRoom(ctx, userID, roomID); err != nil {
		return err
	}

	s.broadcastTypingStarted(ctx, userID, &roomID, nil)
	return nil
}

func (s *Service) StopTypingInRoom(ctx context.Context, userID, roomID uuid.UUID) error {
	if err := s.repo.ClearTypingInRoom(ctx, userID, roomID); err != nil {
		return err
	}

	s.broadcastTypingStopped(userID, &roomID, nil)
	return nil
}

func (s *Service) StartTypingInDM(ctx context.Context, userID, channelID uuid.UUID, otherUserID uuid.UUID) error {
	if err := s.repo.SetTypingInDM(ctx, userID, channelID); err != nil {
		return err
	}

	s.broadcastTypingStartedToUser(ctx, userID, channelID, otherUserID)
	return nil
}

func (s *Service) StopTypingInDM(ctx context.Context, userID, channelID uuid.UUID, otherUserID uuid.UUID) error {
	if err := s.repo.ClearTypingInDM(ctx, userID, channelID); err != nil {
		return err
	}

	s.broadcastTypingStoppedToUser(userID, channelID, otherUserID)
	return nil
}

func (s *Service) broadcastTypingStarted(ctx context.Context, userID uuid.UUID, roomID *uuid.UUID, channelID *uuid.UUID) {
	if s.hub == nil {
		return
	}

	var displayName string
	user, err := s.usersRepo.GetByID(ctx, userID)
	if err == nil {
		displayName = user.DisplayName
	}

	expiresAt := time.Now().Add(TypingDuration)

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_TypingStarted{
			TypingStarted: &streamv1.TypingStarted{
				UserId:          userID.String(),
				UserDisplayName: displayName,
				ExpiresAt:       timestamppb.New(expiresAt),
			},
		},
	}

	if roomID != nil {
		event.GetTypingStarted().RoomId = roomID.String()
		s.hub.BroadcastToRoom(roomID.String(), event)
	}
}

func (s *Service) broadcastTypingStopped(userID uuid.UUID, roomID *uuid.UUID, channelID *uuid.UUID) {
	if s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_TypingStopped{
			TypingStopped: &streamv1.TypingStopped{
				UserId: userID.String(),
			},
		},
	}

	if roomID != nil {
		event.GetTypingStopped().RoomId = roomID.String()
		s.hub.BroadcastToRoom(roomID.String(), event)
	}
}

func (s *Service) broadcastTypingStartedToUser(ctx context.Context, typerID, channelID, recipientID uuid.UUID) {
	if s.hub == nil {
		return
	}

	var displayName string
	user, err := s.usersRepo.GetByID(ctx, typerID)
	if err == nil {
		displayName = user.DisplayName
	}

	expiresAt := time.Now().Add(TypingDuration)

	s.hub.BroadcastToUser(recipientID.String(), &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_TypingStarted{
			TypingStarted: &streamv1.TypingStarted{
				ChannelId:       channelID.String(),
				UserId:          typerID.String(),
				UserDisplayName: displayName,
				ExpiresAt:       timestamppb.New(expiresAt),
			},
		},
	})
}

func (s *Service) broadcastTypingStoppedToUser(typerID, channelID, recipientID uuid.UUID) {
	if s.hub == nil {
		return
	}

	s.hub.BroadcastToUser(recipientID.String(), &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_TypingStopped{
			TypingStopped: &streamv1.TypingStopped{
				ChannelId: channelID.String(),
				UserId:    typerID.String(),
			},
		},
	})
}

func (s *Service) CleanupExpired(ctx context.Context) (int64, error) {
	expired, err := s.repo.GetAndDeleteExpired(ctx)
	if err != nil {
		return 0, err
	}

	for _, ind := range expired {
		if ind.RoomID != nil {
			s.broadcastTypingStopped(ind.UserID, ind.RoomID, nil)
		} else if ind.ChannelID != nil {
			participants, err := s.getDMParticipants(ctx, *ind.ChannelID)
			if err == nil {
				for _, p := range participants {
					if p != ind.UserID {
						s.broadcastTypingStoppedToUser(ind.UserID, *ind.ChannelID, p)
					}
				}
			}
		}
	}

	return int64(len(expired)), nil
}

func (s *Service) getDMParticipants(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error) {
	query := `SELECT user1_id, user2_id FROM dm_channels WHERE id = $1`

	var user1, user2 uuid.UUID
	err := s.repo.pool.QueryRow(ctx, query, channelID).Scan(&user1, &user2)
	if err != nil {
		return nil, err
	}

	return []uuid.UUID{user1, user2}, nil
}
