package admin

import (
	"context"

	"github.com/Alexander-D-Karpov/concord/internal/audit"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/google/uuid"
)

// GetRoomSettings returns the room's effective settings. The caller must be a
// moderator or admin of the room.
func (s *Service) GetRoomSettings(ctx context.Context, callerUserID, roomID string) (rooms.RoomSettings, error) {
	if err := s.checkModeratorPermission(ctx, callerUserID, roomID); err != nil {
		return rooms.RoomSettings{}, err
	}
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return rooms.RoomSettings{}, errors.BadRequest("invalid room_id")
	}
	return s.roomsRepo.GetSettings(ctx, roomUUID)
}

// UpdateRoomSettings validates and persists a full-replace of the room's settings.
// The caller must be an admin. who_can_* must be "member" or "moderator"; negative
// numeric fields are clamped to 0. It writes an audit record and returns the
// stored settings.
func (s *Service) UpdateRoomSettings(ctx context.Context, callerUserID, roomID string, in rooms.RoomSettings) (rooms.RoomSettings, error) {
	if err := s.checkAdminPermission(ctx, callerUserID, roomID); err != nil {
		return rooms.RoomSettings{}, err
	}
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return rooms.RoomSettings{}, errors.BadRequest("invalid room_id")
	}

	if !validRole(in.WhoCanInvite) {
		return rooms.RoomSettings{}, errors.BadRequest("who_can_invite must be 'member' or 'moderator'")
	}
	if !validRole(in.WhoCanPost) {
		return rooms.RoomSettings{}, errors.BadRequest("who_can_post must be 'member' or 'moderator'")
	}
	in.SlowModeInterval = clampNonNegative(in.SlowModeInterval)
	in.MemberCap = clampNonNegative(in.MemberCap)
	in.RetentionDays = clampNonNegative(in.RetentionDays)

	if err := s.roomsRepo.UpdateSettings(ctx, roomUUID, in); err != nil {
		return rooms.RoomSettings{}, errors.Internal("failed to update room settings", err)
	}

	s.audit.log(ctx, audit.Event{RoomID: roomID, UserID: callerUserID, Action: "room.settings_update", ResourceID: roomID, ResourceType: "room"})

	return s.roomsRepo.GetSettings(ctx, roomUUID)
}

// validRole reports whether a who_can_* value is one of the accepted roles.
func validRole(v string) bool {
	return v == "member" || v == "moderator"
}

// clampNonNegative returns n, or 0 if n is negative.
func clampNonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
