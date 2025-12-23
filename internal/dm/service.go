package dm

import (
	"context"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Service struct {
	repo      *Repository
	usersRepo *users.Repository
	hub       *events.Hub
	logger    *zap.Logger
}

func NewService(repo *Repository, usersRepo *users.Repository, hub *events.Hub, logger *zap.Logger) *Service {
	return &Service{
		repo:      repo,
		usersRepo: usersRepo,
		hub:       hub,
		logger:    logger,
	}
}

func (s *Service) GetOrCreateDM(ctx context.Context, otherUserID string) (*DMChannel, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	user1UUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	user2UUID, err := uuid.Parse(otherUserID)
	if err != nil {
		return nil, errors.BadRequest("invalid other user id")
	}

	if user1UUID == user2UUID {
		return nil, errors.BadRequest("cannot create DM with yourself")
	}

	_, err = s.usersRepo.GetByID(ctx, user2UUID)
	if err != nil {
		return nil, errors.NotFound("user not found")
	}

	channel, err := s.repo.GetOrCreate(ctx, user1UUID, user2UUID)
	if err != nil {
		return nil, errors.Internal("failed to create DM channel", err)
	}

	return channel, nil
}

func (s *Service) ListDMs(ctx context.Context) ([]*DMChannelWithUser, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	return s.repo.ListByUser(ctx, userUUID)
}

func (s *Service) CloseDM(ctx context.Context, channelID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	channelUUID, err := uuid.Parse(channelID)
	if err != nil {
		return errors.BadRequest("invalid channel id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	isParticipant, err := s.repo.IsParticipant(ctx, channelUUID, userUUID)
	if err != nil {
		return errors.Internal("failed to check participation", err)
	}

	if !isParticipant {
		return errors.Forbidden("not a participant of this DM")
	}

	return s.repo.Delete(ctx, channelUUID)
}

func (s *Service) GetChannel(ctx context.Context, channelID string) (*DMChannel, error) {
	channelUUID, err := uuid.Parse(channelID)
	if err != nil {
		return nil, errors.BadRequest("invalid channel id")
	}

	return s.repo.GetByID(ctx, channelUUID)
}
