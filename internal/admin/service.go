package admin

import (
	"context"
	"fmt"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	pool      *pgxpool.Pool
	roomsRepo *rooms.Repository
	hub       *events.Hub
	logger    *zap.Logger
}

func NewService(pool *pgxpool.Pool, roomsRepo *rooms.Repository, hub *events.Hub, logger *zap.Logger) *Service {
	return &Service{
		pool:      pool,
		roomsRepo: roomsRepo,
		hub:       hub,
		logger:    logger,
	}
}

func (s *Service) KickUser(ctx context.Context, adminUserID, roomID, targetUserID string) error {
	if err := s.checkAdminPermission(ctx, adminUserID, roomID); err != nil {
		return err
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room_id")
	}

	targetUUID, err := uuid.Parse(targetUserID)
	if err != nil {
		return errors.BadRequest("invalid user_id")
	}

	if err := s.roomsRepo.RemoveMember(ctx, roomUUID, targetUUID); err != nil {
		return err
	}

	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MemberRemoved{
			MemberRemoved: &streamv1.MemberRemoved{
				RoomId: roomID,
				UserId: targetUserID,
			},
		},
	})

	s.logger.Info("user kicked from room",
		zap.String("admin_user_id", adminUserID),
		zap.String("room_id", roomID),
		zap.String("target_user_id", targetUserID),
	)

	return nil
}

func (s *Service) BanUser(ctx context.Context, adminUserID, roomID, targetUserID string, durationSeconds int64) error {
	if err := s.checkAdminPermission(ctx, adminUserID, roomID); err != nil {
		return err
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room_id")
	}

	targetUUID, err := uuid.Parse(targetUserID)
	if err != nil {
		return errors.BadRequest("invalid user_id")
	}

	var expiresAt *time.Time
	if durationSeconds > 0 {
		t := time.Now().Add(time.Duration(durationSeconds) * time.Second)
		expiresAt = &t
	}

	query := `
		INSERT INTO room_bans (room_id, user_id, banned_by, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (room_id, user_id) DO UPDATE SET
			banned_by = EXCLUDED.banned_by,
			expires_at = EXCLUDED.expires_at,
			created_at = NOW()
	`

	adminUUID, err := uuid.Parse(adminUserID)
	if err != nil {
		return errors.BadRequest("invalid admin user_id")
	}

	_, err = s.pool.Exec(ctx, query, roomUUID, targetUUID, adminUUID, expiresAt)
	if err != nil {
		return errors.Internal("failed to ban user", err)
	}

	if err := s.roomsRepo.RemoveMember(ctx, roomUUID, targetUUID); err != nil {
		s.logger.Warn("failed to remove member during ban", zap.Error(err))
	}

	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MemberRemoved{
			MemberRemoved: &streamv1.MemberRemoved{
				RoomId: roomID,
				UserId: targetUserID,
			},
		},
	})

	s.logger.Info("user banned from room",
		zap.String("admin_user_id", adminUserID),
		zap.String("room_id", roomID),
		zap.String("target_user_id", targetUserID),
		zap.Int64("duration_seconds", durationSeconds),
	)

	return nil
}

func (s *Service) MuteUser(ctx context.Context, adminUserID, roomID, targetUserID string, muted bool) error {
	if err := s.checkModeratorPermission(ctx, adminUserID, roomID); err != nil {
		return err
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room_id")
	}

	targetUUID, err := uuid.Parse(targetUserID)
	if err != nil {
		return errors.BadRequest("invalid user_id")
	}

	query := `
		INSERT INTO room_mutes (room_id, user_id, muted_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (room_id, user_id) DO UPDATE SET
			muted_by = EXCLUDED.muted_by,
			created_at = NOW()
	`

	if !muted {
		query = `DELETE FROM room_mutes WHERE room_id = $1 AND user_id = $2`
	}

	adminUUID, err := uuid.Parse(adminUserID)
	if err != nil {
		return errors.BadRequest("invalid admin user_id")
	}

	if muted {
		_, err = s.pool.Exec(ctx, query, roomUUID, targetUUID, adminUUID)
	} else {
		_, err = s.pool.Exec(ctx, query, roomUUID, targetUUID)
	}

	if err != nil {
		return errors.Internal("failed to mute/unmute user", err)
	}

	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_VoiceStateChanged{
			VoiceStateChanged: &streamv1.VoiceStateChanged{
				RoomId:       roomID,
				UserId:       targetUserID,
				Muted:        muted,
				VideoEnabled: false,
				Speaking:     false,
			},
		},
	})

	s.logger.Info("user mute status changed",
		zap.String("admin_user_id", adminUserID),
		zap.String("room_id", roomID),
		zap.String("target_user_id", targetUserID),
		zap.Bool("muted", muted),
	)

	return nil
}

func (s *Service) checkAdminPermission(ctx context.Context, userID, roomID string) error {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room_id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user_id")
	}

	member, err := s.roomsRepo.GetMember(ctx, roomUUID, userUUID)
	if err != nil {
		return errors.Forbidden("you are not a member of this room")
	}

	if member.Role != "admin" {
		return errors.Forbidden("you must be an admin to perform this action")
	}

	return nil
}

func (s *Service) checkModeratorPermission(ctx context.Context, userID, roomID string) error {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room_id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user_id")
	}

	member, err := s.roomsRepo.GetMember(ctx, roomUUID, userUUID)
	if err != nil {
		return errors.Forbidden("you are not a member of this room")
	}

	if member.Role != "admin" && member.Role != "moderator" {
		return errors.Forbidden("you must be an admin or moderator to perform this action")
	}

	return nil
}

func (s *Service) IsUserBanned(ctx context.Context, roomID, userID string) (bool, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return false, fmt.Errorf("invalid room_id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return false, fmt.Errorf("invalid user_id")
	}

	query := `
		SELECT EXISTS(
			SELECT 1 FROM room_bans 
			WHERE room_id = $1 AND user_id = $2 
			AND (expires_at IS NULL OR expires_at > NOW())
		)
	`

	var banned bool
	if err := s.pool.QueryRow(ctx, query, roomUUID, userUUID).Scan(&banned); err != nil {
		return false, err
	}

	return banned, nil
}
