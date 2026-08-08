package admin

import (
	"context"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/audit"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements room moderation (kick, ban, unban, mute) plus listing of the
// current bans/mutes and the room's audit log. Each action re-checks the caller's
// role against the database before proceeding, broadcasts the outcome through hub,
// and (when an audit logger is configured) records an audit entry. Ban and mute
// storage is delegated to the rooms repository so the same state is enforced at the
// membership and voice-join paths.
type Service struct {
	pool      *pgxpool.Pool
	roomsRepo *rooms.Repository
	hub       *events.Hub
	logger    *zap.Logger
	audit     *auditLoggerAdapter
	voice     VoiceEvictor
}

// VoiceEvictor isolates a user from a room's live voice call. Implemented by
// voiceassign.Service. LeaveVoice must be called before RotateRoomKey clears the
// user's session placement so they are excluded from the member list that receives
// the new key; the remaining members re-key, after which the evicted user (still on
// the old key) can neither decrypt their media nor be decrypted by them. This
// isolates the user from the conversation; it does not force-close their UDP socket
// at the voice server (that would require a voice-server control RPC).
type VoiceEvictor interface {
	LeaveVoice(ctx context.Context, roomID, userID string) error
	RotateRoomKey(ctx context.Context, roomID string) error
}

// SetVoiceEvictor installs the voice evictor used to isolate a kicked/banned user
// from live voice. Until set, moderation actions do not touch voice.
func (s *Service) SetVoiceEvictor(v VoiceEvictor) { s.voice = v }

// evictFromVoice best-effort isolates userID from roomID's live voice call: it
// clears their session placement (so they drop out of the member list) and then
// rotates the room key, after which remaining members re-key and the evicted user
// can no longer participate. It is a no-op when no evictor is configured, and voice
// errors never fail the moderation action.
func (s *Service) evictFromVoice(ctx context.Context, roomID, userID string) {
	if s.voice == nil {
		return
	}
	_ = s.voice.LeaveVoice(ctx, roomID, userID)
	if err := s.voice.RotateRoomKey(ctx, roomID); err != nil {
		s.logger.Warn("failed to rotate room key on eviction", zap.String("room_id", roomID), zap.Error(err))
	}
}

// auditLoggerAdapter wraps *audit.Logger so a nil logger is a safe no-op.
type auditLoggerAdapter struct {
	l *audit.Logger
}

func (a *auditLoggerAdapter) log(ctx context.Context, e audit.Event) {
	if a == nil || a.l == nil {
		return
	}
	// Auditing must never fail the moderation action; the logger records its own
	// errors, so the return is intentionally ignored here.
	_ = a.l.Log(ctx, e)
}

// NewService constructs a Service. auditLogger may be nil, in which case moderation
// actions are not persisted to the audit log (but still broadcast and applied).
func NewService(pool *pgxpool.Pool, roomsRepo *rooms.Repository, hub *events.Hub, logger *zap.Logger, auditLogger *audit.Logger) *Service {
	return &Service{
		pool:      pool,
		roomsRepo: roomsRepo,
		hub:       hub,
		logger:    logger,
		audit:     &auditLoggerAdapter{l: auditLogger},
	}
}

// KickUser removes targetUserID from the room after verifying adminUserID has the
// admin role. It disconnects the target's stream and broadcasts a MemberRemoved
// event; the removal is not durable (the user can rejoin unless also banned).
func (s *Service) KickUser(ctx context.Context, adminUserID, roomID, targetUserID string) error {
	roomUUID, targetUUID, err := s.checkAdminAndParse(ctx, adminUserID, roomID, targetUserID)
	if err != nil {
		return err
	}

	if err := s.roomsRepo.RemoveMember(ctx, roomUUID, targetUUID); err != nil {
		return err
	}

	s.hub.NotifyRoomLeave(targetUserID, roomID)
	s.broadcastMemberRemoved(roomID, targetUserID)
	s.evictFromVoice(ctx, roomID, targetUserID)
	s.audit.log(ctx, audit.Event{RoomID: roomID, UserID: adminUserID, Action: "user.kick", ResourceID: targetUserID, ResourceType: "user"})

	s.logger.Info("user kicked from room",
		zap.String("admin_user_id", adminUserID),
		zap.String("room_id", roomID),
		zap.String("target_user_id", targetUserID),
	)
	return nil
}

// BanUser records a room ban (durationSeconds <= 0 means permanent) after verifying
// adminUserID has the admin role, then removes the target as a member, disconnects
// their stream, broadcasts MemberRemoved, and writes an audit record. A failure to
// remove membership is logged but does not fail the ban.
func (s *Service) BanUser(ctx context.Context, adminUserID, roomID, targetUserID string, durationSeconds int64) error {
	roomUUID, targetUUID, err := s.checkAdminAndParse(ctx, adminUserID, roomID, targetUserID)
	if err != nil {
		return err
	}
	adminUUID, err := uuid.Parse(adminUserID)
	if err != nil {
		return errors.BadRequest("invalid admin user_id")
	}

	var expiresAt *time.Time
	if durationSeconds > 0 {
		t := time.Now().Add(time.Duration(durationSeconds) * time.Second)
		expiresAt = &t
	}

	if err := s.roomsRepo.AddBan(ctx, roomUUID, targetUUID, adminUUID, expiresAt); err != nil {
		return errors.Internal("failed to ban user", err)
	}

	if err := s.roomsRepo.RemoveMember(ctx, roomUUID, targetUUID); err != nil {
		s.logger.Warn("failed to remove member during ban", zap.Error(err))
	}

	s.hub.NotifyRoomLeave(targetUserID, roomID)
	s.broadcastMemberRemoved(roomID, targetUserID)
	s.evictFromVoice(ctx, roomID, targetUserID)
	s.audit.log(ctx, audit.Event{
		RoomID: roomID, UserID: adminUserID, Action: "user.ban", ResourceID: targetUserID, ResourceType: "user",
		Metadata: map[string]interface{}{"duration_seconds": durationSeconds},
	})

	s.logger.Info("user banned from room",
		zap.String("admin_user_id", adminUserID),
		zap.String("room_id", roomID),
		zap.String("target_user_id", targetUserID),
		zap.Int64("duration_seconds", durationSeconds),
	)
	return nil
}

// Unban lifts targetUserID's ban in roomID after verifying adminUserID has the
// admin role, and writes an audit record. Unbanning a user who is not banned is a
// no-op (still authorized) and returns nil.
func (s *Service) Unban(ctx context.Context, adminUserID, roomID, targetUserID string) error {
	roomUUID, targetUUID, err := s.checkAdminAndParse(ctx, adminUserID, roomID, targetUserID)
	if err != nil {
		return err
	}

	if _, err := s.roomsRepo.RemoveBan(ctx, roomUUID, targetUUID); err != nil {
		return errors.Internal("failed to unban user", err)
	}

	s.audit.log(ctx, audit.Event{RoomID: roomID, UserID: adminUserID, Action: "user.unban", ResourceID: targetUserID, ResourceType: "user"})
	s.logger.Info("user unbanned from room",
		zap.String("admin_user_id", adminUserID),
		zap.String("room_id", roomID),
		zap.String("target_user_id", targetUserID),
	)
	return nil
}

// MuteUser adds or removes a room mute for targetUserID (per muted) after verifying
// the caller has admin or moderator role, broadcasts a VoiceStateChanged event, and
// writes an audit record.
func (s *Service) MuteUser(ctx context.Context, adminUserID, roomID, targetUserID string, muted bool) error {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room_id")
	}
	if err := s.checkModeratorPermission(ctx, adminUserID, roomID); err != nil {
		return err
	}
	targetUUID, err := uuid.Parse(targetUserID)
	if err != nil {
		return errors.BadRequest("invalid user_id")
	}
	adminUUID, err := uuid.Parse(adminUserID)
	if err != nil {
		return errors.BadRequest("invalid admin user_id")
	}

	if muted {
		if err := s.roomsRepo.AddMute(ctx, roomUUID, targetUUID, adminUUID); err != nil {
			return errors.Internal("failed to mute user", err)
		}
	} else {
		if _, err := s.roomsRepo.RemoveMute(ctx, roomUUID, targetUUID); err != nil {
			return errors.Internal("failed to unmute user", err)
		}
	}

	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_VoiceStateChanged{
			VoiceStateChanged: &streamv1.VoiceStateChanged{
				RoomId: roomID, UserId: targetUserID, Muted: muted,
			},
		},
	})

	action := "user.mute"
	if !muted {
		action = "user.unmute"
	}
	s.audit.log(ctx, audit.Event{RoomID: roomID, UserID: adminUserID, Action: action, ResourceID: targetUserID, ResourceType: "user"})

	s.logger.Info("user mute status changed",
		zap.String("admin_user_id", adminUserID),
		zap.String("room_id", roomID),
		zap.String("target_user_id", targetUserID),
		zap.Bool("muted", muted),
	)
	return nil
}

// ListBans returns the active bans in roomID. The caller must be a moderator or
// admin of the room.
func (s *Service) ListBans(ctx context.Context, callerUserID, roomID string) ([]rooms.Ban, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room_id")
	}
	if err := s.checkModeratorPermission(ctx, callerUserID, roomID); err != nil {
		return nil, err
	}
	return s.roomsRepo.ListBans(ctx, roomUUID)
}

// ListMutes returns the current mutes in roomID. The caller must be a moderator or
// admin of the room.
func (s *Service) ListMutes(ctx context.Context, callerUserID, roomID string) ([]rooms.Mute, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room_id")
	}
	if err := s.checkModeratorPermission(ctx, callerUserID, roomID); err != nil {
		return nil, err
	}
	return s.roomsRepo.ListMutes(ctx, roomUUID)
}

// ListAuditLog returns the room's audit entries, newest first. The caller must be a
// moderator or admin of the room. When no audit logger is configured it returns an
// empty slice.
func (s *Service) ListAuditLog(ctx context.Context, callerUserID, roomID string, limit, offset int) ([]audit.Event, error) {
	if err := s.checkModeratorPermission(ctx, callerUserID, roomID); err != nil {
		return nil, err
	}
	if s.audit == nil || s.audit.l == nil {
		return nil, nil
	}
	return s.audit.l.List(ctx, roomID, limit, offset)
}

// IsUserBanned reports whether userID has an active ban in roomID.
func (s *Service) IsUserBanned(ctx context.Context, roomID, userID string) (bool, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return false, errors.BadRequest("invalid room_id")
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return false, errors.BadRequest("invalid user_id")
	}
	return s.roomsRepo.IsBanned(ctx, roomUUID, userUUID)
}

// broadcastMemberRemoved emits a MemberRemoved event for the room.
func (s *Service) broadcastMemberRemoved(roomID, targetUserID string) {
	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MemberRemoved{
			MemberRemoved: &streamv1.MemberRemoved{RoomId: roomID, UserId: targetUserID},
		},
	})
}

// checkAdminAndParse verifies adminUserID has the admin role in roomID and parses
// the room and target IDs, returning them for use by the caller.
func (s *Service) checkAdminAndParse(ctx context.Context, adminUserID, roomID, targetUserID string) (uuid.UUID, uuid.UUID, error) {
	if err := s.checkAdminPermission(ctx, adminUserID, roomID); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return uuid.Nil, uuid.Nil, errors.BadRequest("invalid room_id")
	}
	targetUUID, err := uuid.Parse(targetUserID)
	if err != nil {
		return uuid.Nil, uuid.Nil, errors.BadRequest("invalid user_id")
	}
	return roomUUID, targetUUID, nil
}

// checkAdminPermission returns nil only if userID is a member of roomID with the
// "admin" role, and Forbidden otherwise; it reads the membership fresh from the DB.
func (s *Service) checkAdminPermission(ctx context.Context, userID, roomID string) error {
	member, err := s.getMember(ctx, userID, roomID)
	if err != nil {
		return err
	}
	if member.Role != "admin" {
		return errors.Forbidden("you must be an admin to perform this action")
	}
	return nil
}

// checkModeratorPermission returns nil only if userID is a member of roomID with the
// "admin" or "moderator" role, and Forbidden otherwise; it reads the membership
// fresh from the DB.
func (s *Service) checkModeratorPermission(ctx context.Context, userID, roomID string) error {
	member, err := s.getMember(ctx, userID, roomID)
	if err != nil {
		return err
	}
	if member.Role != "admin" && member.Role != "moderator" {
		return errors.Forbidden("you must be an admin or moderator to perform this action")
	}
	return nil
}

// getMember parses the IDs and loads the caller's membership, returning Forbidden
// when they are not a member of the room.
func (s *Service) getMember(ctx context.Context, userID, roomID string) (*rooms.Member, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room_id")
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user_id")
	}
	member, err := s.roomsRepo.GetMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, errors.Forbidden("you are not a member of this room")
	}
	return member, nil
}
