package call

import (
	"context"
	"strings"
	"time"

	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
	"github.com/google/uuid"
)

type Handler struct {
	callv1.UnimplementedCallServiceServer
	voiceService *voiceassign.Service
	roomRepo     *rooms.Repository
}

func NewHandler(voiceService *voiceassign.Service, roomRepo *rooms.Repository) *Handler {
	return &Handler{
		voiceService: voiceService,
		roomRepo:     roomRepo,
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
		return nil, errors.ToGRPCError(errors.BadRequest("invalid room id"))
	}

	room, err := h.roomRepo.GetByID(ctx, roomUUID)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	var server *voiceassign.VoiceServer
	if room.VoiceServerID != nil {
		server, err = h.voiceService.GetServerByID(ctx, room.VoiceServerID.String())
		if err != nil {
			return nil, errors.ToGRPCError(err)
		}
	} else {
		server, err = h.voiceService.SelectServerForRoom(ctx, req.RoomId, room.Region)
		if err != nil {
			return nil, errors.ToGRPCError(err)
		}
	}

	token, key, err := h.voiceService.IssueJoinToken(userID, req.RoomId, server.ID.String(), 5*time.Minute)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to issue token", err))
	}

	parts := strings.Split(server.AddrUDP, ":")
	host := parts[0]
	port := uint32(50000)

	return &callv1.JoinVoiceResponse{
		Endpoint: &callv1.UdpEndpoint{
			Host: host,
			Port: port,
		},
		ServerId:   server.ID.String(),
		VoiceToken: token,
		Codec: &callv1.CodecHint{
			Audio: "opus",
			Video: "h264",
		},
		Crypto: &callv1.CryptoSuite{
			Aead:        "chacha20-poly1305",
			KeyMaterial: key,
		},
		Participants: []*callv1.Participant{},
	}, nil
}

func (h *Handler) LeaveVoice(ctx context.Context, req *callv1.LeaveVoiceRequest) (*callv1.EmptyResponse, error) {
	return &callv1.EmptyResponse{}, nil
}

func (h *Handler) SetMediaPrefs(ctx context.Context, req *callv1.SetMediaPrefsRequest) (*callv1.EmptyResponse, error) {
	return &callv1.EmptyResponse{}, nil
}
