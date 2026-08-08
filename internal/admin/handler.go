package admin

import (
	"context"

	adminv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/admin/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Handler is the gRPC AdminService server; it validates requests, resolves the
// calling admin from the context, and delegates moderation to Service.
type Handler struct {
	adminv1.UnimplementedAdminServiceServer
	service *Service
}

// NewHandler returns a Handler backed by the given Service.
func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

// caller returns the authenticated user ID from the context, or an error if the
// request is unauthenticated.
func caller(ctx context.Context) (string, error) {
	id := interceptor.GetUserID(ctx)
	if id == "" {
		return "", errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}
	return id, nil
}

// requireIDs validates that room_id and user_id are present.
func requireIDs(roomID, userID string) error {
	if roomID == "" {
		return status.Error(codes.InvalidArgument, "room_id is required")
	}
	if userID == "" {
		return status.Error(codes.InvalidArgument, "user_id is required")
	}
	return nil
}

// Kick removes req.UserId from req.RoomId on behalf of the authenticated admin.
func (h *Handler) Kick(ctx context.Context, req *adminv1.KickRequest) (*adminv1.EmptyResponse, error) {
	if err := requireIDs(req.RoomId, req.UserId); err != nil {
		return nil, err
	}
	adminUserID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.service.KickUser(ctx, adminUserID, req.RoomId, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &adminv1.EmptyResponse{}, nil
}

// Ban bans req.UserId from req.RoomId for req.DurationSeconds (0 = permanent).
func (h *Handler) Ban(ctx context.Context, req *adminv1.BanRequest) (*adminv1.EmptyResponse, error) {
	if err := requireIDs(req.RoomId, req.UserId); err != nil {
		return nil, err
	}
	adminUserID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.service.BanUser(ctx, adminUserID, req.RoomId, req.UserId, req.DurationSeconds); err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &adminv1.EmptyResponse{}, nil
}

// Unban lifts req.UserId's ban in req.RoomId on behalf of the authenticated admin.
func (h *Handler) Unban(ctx context.Context, req *adminv1.UnbanRequest) (*adminv1.EmptyResponse, error) {
	if err := requireIDs(req.RoomId, req.UserId); err != nil {
		return nil, err
	}
	adminUserID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.service.Unban(ctx, adminUserID, req.RoomId, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &adminv1.EmptyResponse{}, nil
}

// Mute sets req.UserId's muted state in req.RoomId per req.Muted.
func (h *Handler) Mute(ctx context.Context, req *adminv1.MuteRequest) (*adminv1.EmptyResponse, error) {
	if err := requireIDs(req.RoomId, req.UserId); err != nil {
		return nil, err
	}
	adminUserID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.service.MuteUser(ctx, adminUserID, req.RoomId, req.UserId, req.Muted); err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &adminv1.EmptyResponse{}, nil
}

// ListBans returns the active bans in req.RoomId for a moderator/admin caller.
func (h *Handler) ListBans(ctx context.Context, req *adminv1.ListBansRequest) (*adminv1.ListBansResponse, error) {
	if req.RoomId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id is required")
	}
	callerID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	bans, err := h.service.ListBans(ctx, callerID, req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	resp := &adminv1.ListBansResponse{Bans: make([]*adminv1.BanEntry, 0, len(bans))}
	for _, b := range bans {
		var expires int64
		if b.ExpiresAt != nil {
			expires = b.ExpiresAt.Unix()
		}
		resp.Bans = append(resp.Bans, &adminv1.BanEntry{
			UserId:    b.UserID.String(),
			BannedBy:  b.BannedBy.String(),
			ExpiresAt: expires,
			CreatedAt: b.CreatedAt.Unix(),
		})
	}
	return resp, nil
}

// ListMutes returns the current mutes in req.RoomId for a moderator/admin caller.
func (h *Handler) ListMutes(ctx context.Context, req *adminv1.ListMutesRequest) (*adminv1.ListMutesResponse, error) {
	if req.RoomId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id is required")
	}
	callerID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	mutes, err := h.service.ListMutes(ctx, callerID, req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	resp := &adminv1.ListMutesResponse{Mutes: make([]*adminv1.MuteEntry, 0, len(mutes))}
	for _, m := range mutes {
		resp.Mutes = append(resp.Mutes, &adminv1.MuteEntry{
			UserId:    m.UserID.String(),
			MutedBy:   m.MutedBy.String(),
			CreatedAt: m.CreatedAt.Unix(),
		})
	}
	return resp, nil
}

// ListAuditLog returns req.RoomId's audit entries (newest first) for a
// moderator/admin caller.
func (h *Handler) ListAuditLog(ctx context.Context, req *adminv1.ListAuditLogRequest) (*adminv1.ListAuditLogResponse, error) {
	if req.RoomId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id is required")
	}
	callerID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := h.service.ListAuditLog(ctx, callerID, req.RoomId, int(req.Limit), int(req.Offset))
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	resp := &adminv1.ListAuditLogResponse{Entries: make([]*adminv1.AuditLogEntry, 0, len(entries))}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, &adminv1.AuditLogEntry{
			Id:         e.ID.String(),
			ActorId:    e.UserID,
			Action:     e.Action,
			TargetId:   e.ResourceID,
			TargetType: e.ResourceType,
			CreatedAt:  e.Timestamp.Unix(),
		})
	}
	return resp, nil
}

// GetRoomSettings returns req.RoomId's settings for a moderator/admin caller.
func (h *Handler) GetRoomSettings(ctx context.Context, req *adminv1.GetRoomSettingsRequest) (*adminv1.GetRoomSettingsResponse, error) {
	if req.RoomId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id is required")
	}
	callerID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := h.service.GetRoomSettings(ctx, callerID, req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &adminv1.GetRoomSettingsResponse{Settings: settingsToProto(settings)}, nil
}

// UpdateRoomSettings applies a full-replace of req.RoomId's settings for an admin
// caller and returns the stored result.
func (h *Handler) UpdateRoomSettings(ctx context.Context, req *adminv1.UpdateRoomSettingsRequest) (*adminv1.UpdateRoomSettingsResponse, error) {
	if req.RoomId == "" {
		return nil, status.Error(codes.InvalidArgument, "room_id is required")
	}
	if req.Settings == nil {
		return nil, status.Error(codes.InvalidArgument, "settings is required")
	}
	callerID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := h.service.UpdateRoomSettings(ctx, callerID, req.RoomId, settingsFromProto(req.Settings))
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &adminv1.UpdateRoomSettingsResponse{Settings: settingsToProto(settings)}, nil
}

// settingsToProto maps the domain settings to the wire message.
func settingsToProto(s rooms.RoomSettings) *adminv1.RoomSettings {
	return &adminv1.RoomSettings{
		SlowModeInterval:    int32(s.SlowModeInterval),
		WhoCanInvite:        s.WhoCanInvite,
		WhoCanPost:          s.WhoCanPost,
		IsPrivate:           s.IsPrivate,
		RequireApproval:     s.RequireApproval,
		MemberCap:           int32(s.MemberCap),
		RetentionDays:       int32(s.RetentionDays),
		LinkPreviewsEnabled: s.LinkPreviewsEnabled,
		GifsEnabled:         s.GifsEnabled,
		StickersEnabled:     s.StickersEnabled,
		WordFilters:         s.WordFilters,
	}
}

// settingsFromProto maps the wire message to the domain settings.
func settingsFromProto(p *adminv1.RoomSettings) rooms.RoomSettings {
	return rooms.RoomSettings{
		SlowModeInterval:    int(p.SlowModeInterval),
		WhoCanInvite:        p.WhoCanInvite,
		WhoCanPost:          p.WhoCanPost,
		IsPrivate:           p.IsPrivate,
		RequireApproval:     p.RequireApproval,
		MemberCap:           int(p.MemberCap),
		RetentionDays:       int(p.RetentionDays),
		LinkPreviewsEnabled: p.LinkPreviewsEnabled,
		GifsEnabled:         p.GifsEnabled,
		StickersEnabled:     p.StickersEnabled,
		WordFilters:         p.WordFilters,
	}
}
