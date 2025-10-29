package call

import (
	"context"
	"fmt"
	"github.com/google/uuid"

	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
	"go.uber.org/zap"
)

type Handler struct {
	callv1.UnimplementedCallServiceServer
	voiceAssign *voiceassign.Service
	roomsRepo   *rooms.Repository
	logger      *zap.Logger
}

func NewHandler(voiceAssign *voiceassign.Service, roomsRepo *rooms.Repository) *Handler {
	return &Handler{
		voiceAssign: voiceAssign,
		roomsRepo:   roomsRepo,
		logger:      zap.L(),
	}
}

func (h *Handler) JoinVoice(ctx context.Context, req *callv1.JoinVoiceRequest) (*callv1.JoinVoiceResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	roomUUID, err := uuid.Parse(req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid room_id"))
	}

	_, err = h.roomsRepo.GetByID(ctx, roomUUID)
	if err != nil {
		h.logger.Error("failed to get room", zap.Error(err))
		return nil, errors.ToGRPCError(errors.NotFound("room not found"))
	}

	h.logger.Info("assigning voice server",
		zap.String("user_id", userID),
		zap.String("room_id", req.RoomId),
		zap.Bool("audio_only", req.AudioOnly),
	)

	assignment, err := h.voiceAssign.AssignVoiceServer(ctx, userID, req.RoomId)
	if err != nil {
		h.logger.Error("failed to assign voice server", zap.Error(err))
		return nil, errors.ToGRPCError(errors.Internal("failed to assign voice server", err))
	}

	h.logger.Info("voice server assigned",
		zap.String("user_id", userID),
		zap.String("server_id", assignment.ServerID),
		zap.String("endpoint", fmt.Sprintf("%s:%d", assignment.Host, assignment.Port)),
		zap.String("token", assignment.Token[:20]+"..."),
	)

	participants := make([]*callv1.Participant, len(assignment.Participants))
	for i, p := range assignment.Participants {
		participants[i] = &callv1.Participant{
			UserId:       p.UserID,
			Ssrc:         p.SSRC,
			Muted:        p.Muted,
			VideoEnabled: p.VideoEnabled,
		}
	}

	return &callv1.JoinVoiceResponse{
		Endpoint: &callv1.UdpEndpoint{
			Host: assignment.Host,
			Port: assignment.Port,
		},
		ServerId:   assignment.ServerID,
		VoiceToken: assignment.Token,
		Codec: &callv1.CodecHint{
			Audio: "opus",
			Video: "h264",
		},
		Crypto: &callv1.CryptoSuite{
			Aead:        "chacha20-poly1305",
			KeyId:       assignment.KeyID,
			KeyMaterial: assignment.KeyMaterial,
			NonceBase:   assignment.NonceBase,
		},
		Participants: participants,
	}, nil
}
