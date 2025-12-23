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

type Handler struct {
	callv1.UnimplementedCallServiceServer
	voiceAssign *voiceassign.Service
	roomsRepo   *rooms.Repository
	hub         *events.Hub
	logger      *zap.Logger
}

func NewHandler(va *voiceassign.Service, rr *rooms.Repository, hub *events.Hub, logger *zap.Logger) *Handler {
	return &Handler{
		voiceAssign: va,
		roomsRepo:   rr,
		hub:         hub,
		logger:      logger,
	}
}

func (h *Handler) JoinVoice(ctx context.Context, req *callv1.JoinVoiceRequest) (*callv1.JoinVoiceResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	roomUUID, err := uuid.Parse(req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid room id"))
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	isMember, err := h.roomsRepo.IsMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to check membership", err))
	}
	if !isMember {
		return nil, errors.ToGRPCError(errors.Forbidden("not a member of this room"))
	}

	assignment, err := h.voiceAssign.AssignToVoice(ctx, req.RoomId, userID, req.AudioOnly)
	if err != nil {
		h.logger.Error("failed to assign voice server",
			zap.String("room_id", req.RoomId),
			zap.String("user_id", userID),
			zap.Error(err),
		)
		return nil, errors.ToGRPCError(errors.Internal("failed to join voice", err))
	}

	h.logger.Info("user joining voice",
		zap.String("room_id", req.RoomId),
		zap.String("user_id", userID),
		zap.String("server_id", assignment.ServerID),
	)

	if h.hub != nil {
		h.hub.BroadcastToRoom(req.RoomId, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_VoiceUserJoined{
				VoiceUserJoined: &streamv1.VoiceUserJoined{
					RoomId:    req.RoomId,
					UserId:    userID,
					AudioOnly: req.AudioOnly,
				},
			},
		})
	}

	participants, _ := h.voiceAssign.GetVoiceParticipants(ctx, req.RoomId)
	protoParticipants := make([]*callv1.Participant, 0, len(participants))
	for _, p := range participants {
		if p.UserID != userID {
			protoParticipants = append(protoParticipants, &callv1.Participant{
				UserId:       p.UserID,
				Ssrc:         0,
				Muted:        p.Muted,
				VideoEnabled: p.VideoEnabled,
			})
		}
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
	}, nil
}

func (h *Handler) LeaveVoice(ctx context.Context, req *callv1.LeaveVoiceRequest) (*callv1.EmptyResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	if err := h.voiceAssign.LeaveVoice(ctx, req.RoomId, userID); err != nil {
		h.logger.Warn("failed to leave voice",
			zap.String("room_id", req.RoomId),
			zap.String("user_id", userID),
			zap.Error(err),
		)
	}

	h.logger.Info("user left voice",
		zap.String("room_id", req.RoomId),
		zap.String("user_id", userID),
	)

	if h.hub != nil {
		h.hub.BroadcastToRoom(req.RoomId, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_VoiceUserLeft{
				VoiceUserLeft: &streamv1.VoiceUserLeft{
					RoomId: req.RoomId,
					UserId: userID,
				},
			},
		})
	}

	return &callv1.EmptyResponse{}, nil
}

func (h *Handler) SetMediaPrefs(ctx context.Context, req *callv1.SetMediaPrefsRequest) (*callv1.EmptyResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	if err := h.voiceAssign.UpdateMediaPrefs(ctx, req.RoomId, userID, req.Muted, req.VideoEnabled); err != nil {
		h.logger.Warn("failed to update media prefs",
			zap.String("room_id", req.RoomId),
			zap.String("user_id", userID),
			zap.Error(err),
		)
	}

	if h.hub != nil {
		h.hub.BroadcastToRoom(req.RoomId, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_VoiceStateChanged{
				VoiceStateChanged: &streamv1.VoiceStateChanged{
					RoomId:       req.RoomId,
					UserId:       userID,
					Muted:        req.Muted,
					VideoEnabled: req.VideoEnabled,
					Speaking:     false,
				},
			},
		})
	}

	return &callv1.EmptyResponse{}, nil
}

func (h *Handler) GetVoiceStatus(ctx context.Context, req *callv1.GetVoiceStatusRequest) (*callv1.GetVoiceStatusResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	participants, err := h.voiceAssign.GetVoiceParticipants(ctx, req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to get voice status", err))
	}

	protoParticipants := make([]*callv1.VoiceParticipant, len(participants))
	for i, p := range participants {
		protoParticipants[i] = &callv1.VoiceParticipant{
			UserId:       p.UserID,
			Muted:        p.Muted,
			VideoEnabled: p.VideoEnabled,
			Speaking:     p.Speaking,
			JoinedAt:     timestamppb.New(time.Unix(p.JoinedAt, 0)),
		}
	}

	return &callv1.GetVoiceStatusResponse{
		Participants:      protoParticipants,
		TotalParticipants: int32(len(participants)),
	}, nil
}
