package call

import (
	"context"
	"time"

	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// memberChecker is the narrow slice of the rooms repository the voice gate needs.
// Depending on the interface (not the concrete *rooms.Repository) keeps the access
// check unit-testable without a database.
type memberChecker interface {
	IsMember(ctx context.Context, roomID, userID uuid.UUID) (bool, error)
	IsBanned(ctx context.Context, roomID, userID uuid.UUID) (bool, error)
}

// Handler is the CallService gRPC server. It owns no state beyond its
// dependencies: voice work is delegated to voiceAssign, membership checks to
// roomsRepo, and client fan-out events to hub.
type Handler struct {
	callv1.UnimplementedCallServiceServer
	voiceAssign *voiceassign.Service
	roomsRepo   memberChecker
	hub         *events.Hub
	logger      *zap.Logger
	// debug enables the VOICE_DEBUG fast-join path: voice RPCs require only an
	// authenticated user and skip the room-membership check, so the throughput
	// harness can join without the invite dance. Off by default; MUST stay off in
	// production or anyone can join any room's voice.
	debug bool
}

// NewHandler wires the CallService handler. debug comes from VOICE_DEBUG and,
// when true, opens the membership-check bypass in requireVoiceAccess; it must be
// false in production.
func NewHandler(va *voiceassign.Service, rr *rooms.Repository, hub *events.Hub, logger *zap.Logger, debug bool) *Handler {
	return &Handler{
		voiceAssign: va,
		roomsRepo:   rr,
		hub:         hub,
		logger:      logger,
		debug:       debug,
	}
}

// requireVoiceAccess authorizes a voice RPC. Normally the caller must be a member of
// the room. When VOICE_DEBUG is enabled it requires only an authenticated user and
// skips the membership check, so the throughput harness can fast-join. The bypass is
// gated by h.debug (false unless VOICE_DEBUG=true), so in production this is exactly
// requireMember and the mass-join DoS path stays closed.
func (h *Handler) requireVoiceAccess(ctx context.Context, roomID string) (string, error) {
	if h.debug {
		userID, err := h.requireAuthed(ctx, roomID)
		if err != nil {
			return "", err
		}
		h.logger.Warn("VOICE_DEBUG: skipping room-membership check for voice access",
			zap.String("room_id", roomID), zap.String("user_id", userID))
		return userID, nil
	}
	return h.requireMember(ctx, roomID)
}

// requireAuthed validates that the request is authenticated and carries a
// well-formed room id, returning the caller's user id. It performs no membership
// check.
func (h *Handler) requireAuthed(ctx context.Context, roomID string) (string, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return "", errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}
	if roomID == "" {
		return "", errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}
	if _, err := uuid.Parse(roomID); err != nil {
		return "", errors.ToGRPCError(errors.BadRequest("invalid room id"))
	}
	return userID, nil
}

// requireMember authorizes the strictest tier: the caller must be authenticated,
// pass a valid room id, and be a current member of that room per roomsRepo.
// Returns Forbidden if not a member and Internal if the membership lookup fails.
func (h *Handler) requireMember(ctx context.Context, roomID string) (string, error) {
	userID, err := h.requireAuthed(ctx, roomID)
	if err != nil {
		return "", err
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return "", errors.ToGRPCError(errors.BadRequest("invalid room id"))
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return "", errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	isMember, err := h.roomsRepo.IsMember(ctx, roomUUID, userUUID)
	if err != nil {
		return "", errors.ToGRPCError(errors.Internal("failed to check membership", err))
	}
	if !isMember {
		return "", errors.ToGRPCError(errors.Forbidden("not a member of this room"))
	}

	// A banned user is refused voice even if a membership row still exists.
	banned, err := h.roomsRepo.IsBanned(ctx, roomUUID, userUUID)
	if err != nil {
		return "", errors.ToGRPCError(errors.Internal("failed to check ban status", err))
	}
	if banned {
		return "", errors.ToGRPCError(errors.Forbidden("you are banned from this room"))
	}

	return userID, nil
}

// JoinVoice authorizes the caller (requireVoiceAccess), assigns them to a voice
// server via voiceAssign, and returns the UDP/TCP endpoint, voice token, codec,
// and crypto suite the client needs to connect. The caller's own state is
// stripped from the returned participant list and, on success, a VoiceUserJoined
// event is broadcast to the room. If participant loading fails the join still
// succeeds with a synthesized self state.
func (h *Handler) JoinVoice(ctx context.Context, req *callv1.JoinVoiceRequest) (*callv1.JoinVoiceResponse, error) {
	userID, err := h.requireVoiceAccess(ctx, req.RoomId)
	if err != nil {
		return nil, err
	}

	assignment, err := h.voiceAssign.AssignToVoice(ctx, req.RoomId, userID, req.GetRegion(), req.AudioOnly)
	if err != nil {
		h.logger.Error("failed to assign voice server",
			zap.String("room_id", req.RoomId),
			zap.String("user_id", userID),
			zap.Error(err),
		)
		return nil, errors.ToGRPCError(err)
	}

	h.logger.Info("user joining voice",
		zap.String("room_id", req.RoomId),
		zap.String("user_id", userID),
		zap.String("server_id", assignment.ServerID),
	)

	participants, perr := h.voiceAssign.GetVoiceParticipants(ctx, req.RoomId)
	if perr != nil {
		h.logger.Warn("failed to load voice participants after join",
			zap.String("room_id", req.RoomId), zap.Error(perr))
	}

	var self *streamv1.VoiceParticipantState
	protoParticipants := make([]*callv1.Participant, 0, len(participants))
	for _, p := range participants {
		if p.UserID == userID {
			self = ToParticipantState(p)
			continue
		}
		protoParticipants = append(protoParticipants, &callv1.Participant{
			UserId:        p.UserID,
			Muted:         p.Muted,
			VideoEnabled:  p.VideoEnabled,
			ScreenSharing: p.ScreenSharing,
		})
	}

	if self == nil {
		self = &streamv1.VoiceParticipantState{
			UserId:       userID,
			VideoEnabled: !req.AudioOnly,
			JoinedAt:     timestamppb.Now(),
		}
	}

	if h.hub != nil {
		h.hub.BroadcastToRoom(req.RoomId, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_VoiceUserJoined{
				VoiceUserJoined: &streamv1.VoiceUserJoined{
					RoomId:      req.RoomId,
					UserId:      userID,
					AudioOnly:   req.AudioOnly,
					Participant: self,
				},
			},
		})
	}

	return &callv1.JoinVoiceResponse{
		Endpoint: &callv1.UdpEndpoint{
			Host: assignment.Endpoint.Host,
			Port: uint32(assignment.Endpoint.Port),
		},
		ServerId:   assignment.ServerID,
		VoiceToken: assignment.VoiceToken,
		Codec: &callv1.CodecHint{
			Audio: assignment.Codec.Audio,
			Video: assignment.Codec.Video,
		},
		Crypto: &callv1.CryptoSuite{
			Aead:        assignment.Crypto.AEAD,
			KeyId:       assignment.Crypto.KeyID,
			KeyMaterial: assignment.Crypto.KeyMaterial,
			NonceBase:   assignment.Crypto.NonceBase,
		},
		Participants: protoParticipants,
		ExpiresIn:    uint32(assignment.ExpiresIn),
		TcpEndpoint:  tcpEndpoint(assignment.TCPEndpoint),
	}, nil
}

// tcpEndpoint converts a voiceassign TCP fallback endpoint to its proto form,
// returning nil when the endpoint is unset (empty host or zero port) so the
// field is omitted rather than sent as a zero endpoint.
func tcpEndpoint(e voiceassign.UDPEndpoint) *callv1.UdpEndpoint {
	if e.Host == "" || e.Port == 0 {
		return nil
	}
	return &callv1.UdpEndpoint{Host: e.Host, Port: uint32(e.Port)}
}

// LeaveVoice authorizes the caller, removes them from the voice session, and
// broadcasts VoiceUserLeft to the room. A voiceAssign failure is surfaced as
// Internal; the broadcast is skipped only when no hub is configured.
func (h *Handler) LeaveVoice(ctx context.Context, req *callv1.LeaveVoiceRequest) (*callv1.EmptyResponse, error) {
	userID, err := h.requireVoiceAccess(ctx, req.RoomId)
	if err != nil {
		return nil, err
	}

	if err := h.voiceAssign.LeaveVoice(ctx, req.RoomId, userID); err != nil {
		h.logger.Warn("failed to leave voice",
			zap.String("room_id", req.RoomId), zap.String("user_id", userID), zap.Error(err))
		return nil, errors.ToGRPCError(errors.Internal("failed to leave voice", err))
	}

	if h.hub != nil {
		h.hub.BroadcastToRoom(req.RoomId, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_VoiceUserLeft{
				VoiceUserLeft: &streamv1.VoiceUserLeft{RoomId: req.RoomId, UserId: userID},
			},
		})
	}

	return &callv1.EmptyResponse{}, nil
}

// SetMediaPrefs updates the caller's mute/video/screen-share flags in the voice
// session and broadcasts a VoiceStateChanged event (with Speaking forced false)
// to the room so peers reflect the new media state.
func (h *Handler) SetMediaPrefs(ctx context.Context, req *callv1.SetMediaPrefsRequest) (*callv1.EmptyResponse, error) {
	userID, err := h.requireVoiceAccess(ctx, req.RoomId)
	if err != nil {
		return nil, err
	}

	if err := h.voiceAssign.UpdateMediaPrefs(ctx, req.RoomId, userID, req.Muted, req.VideoEnabled, req.ScreenSharing); err != nil {
		h.logger.Warn("failed to update media prefs",
			zap.String("room_id", req.RoomId), zap.String("user_id", userID), zap.Error(err))
		return nil, errors.ToGRPCError(errors.Internal("failed to update media prefs", err))
	}

	if h.hub != nil {
		h.hub.BroadcastToRoom(req.RoomId, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_VoiceStateChanged{
				VoiceStateChanged: &streamv1.VoiceStateChanged{
					RoomId:        req.RoomId,
					UserId:        userID,
					Muted:         req.Muted,
					VideoEnabled:  req.VideoEnabled,
					ScreenSharing: req.ScreenSharing,
					Speaking:      false,
				},
			},
		})
	}

	return &callv1.EmptyResponse{}, nil
}

// GetVoiceStatus returns the current voice participants for a room after an
// access check. It is read-only and broadcasts nothing; JoinedAt is converted
// from the stored Unix seconds to a proto timestamp.
func (h *Handler) GetVoiceStatus(ctx context.Context, req *callv1.GetVoiceStatusRequest) (*callv1.GetVoiceStatusResponse, error) {
	if _, err := h.requireVoiceAccess(ctx, req.RoomId); err != nil {
		return nil, err
	}

	participants, err := h.voiceAssign.GetVoiceParticipants(ctx, req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to get voice status", err))
	}

	protoParticipants := make([]*callv1.VoiceParticipant, len(participants))
	for i, p := range participants {
		protoParticipants[i] = &callv1.VoiceParticipant{
			UserId:        p.UserID,
			Muted:         p.Muted,
			VideoEnabled:  p.VideoEnabled,
			ScreenSharing: p.ScreenSharing,
			Speaking:      p.Speaking,
			JoinedAt:      timestamppb.New(time.Unix(p.JoinedAt, 0)),
		}
	}

	return &callv1.GetVoiceStatusResponse{
		Participants:      protoParticipants,
		TotalParticipants: int32(len(participants)),
	}, nil
}
