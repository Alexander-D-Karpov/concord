package admin

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool      *pgxpool.Pool
	roomsRepo *rooms.Repository
}

func NewService(pool *pgxpool.Pool, roomsRepo *rooms.Repository) *Service {
	return &Service{
		pool:      pool,
		roomsRepo: roomsRepo,
	}
}

func (s *Service) Kick(ctx context.Context, roomID, userID string) error {
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

	member, err := s.roomsRepo.GetMember(ctx, roomUUID, callerUUID)
	if err != nil {
		return err
	}

	if member.Role != "admin" {
		return errors.Forbidden("only admins can kick users")
	}

	targetUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	return s.roomsRepo.RemoveMember(ctx, roomUUID, targetUUID)
}

func (s *Service) Ban(ctx context.Context, roomID, userID string, durationSeconds int64) error {
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

	member, err := s.roomsRepo.GetMember(ctx, roomUUID, callerUUID)
	if err != nil {
		return err
	}

	if member.Role != "admin" {
		return errors.Forbidden("only admins can ban users")
	}

	targetUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	bannedUntil := time.Now().Add(time.Duration(durationSeconds) * time.Second)

	_, err = s.pool.Exec(ctx, `
		UPDATE memberships 
		SET banned_until = $3 
		WHERE room_id = $1 AND user_id = $2
	`, roomUUID, targetUUID, bannedUntil)

	return err
}

func (s *Service) Mute(ctx context.Context, roomID, userID string, muted bool) error {
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

	member, err := s.roomsRepo.GetMember(ctx, roomUUID, callerUUID)
	if err != nil {
		return err
	}

	if member.Role != "admin" {
		return errors.Forbidden("only admins can mute users")
	}

	return nil
}
