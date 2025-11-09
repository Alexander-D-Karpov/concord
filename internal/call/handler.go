package call

import (
	"context"
	"sync"

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
	voiceStates *VoiceStateManager
	hub         *events.Hub
	logger      *zap.Logger
}

type VoiceStateManager struct {
	mu     sync.RWMutex
	states map[string]map[string]*VoiceState
}

type VoiceState struct {
	UserID       string
	RoomID       string
	ServerID     string
	AudioOnly    bool
	Muted        bool
	VideoEnabled bool
	Speaking     bool
	JoinedAt     timestamppb.Timestamp
}

func NewVoiceStateManager() *VoiceStateManager {
	return &VoiceStateManager{
		states: make(map[string]map[string]*VoiceState),
	}
}

func (m *VoiceStateManager) AddUser(roomID, userID, serverID string, audioOnly bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.states[roomID] == nil {
		m.states[roomID] = make(map[string]*VoiceState)
	}

	m.states[roomID][userID] = &VoiceState{
		UserID:       userID,
		RoomID:       roomID,
		ServerID:     serverID,
		AudioOnly:    audioOnly,
		Muted:        false,
		VideoEnabled: !audioOnly,
		Speaking:     false,
		JoinedAt:     *timestamppb.Now(),
	}
}

func (m *VoiceStateManager) RemoveUser(roomID, userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.states[roomID] != nil {
		delete(m.states[roomID], userID)
		if len(m.states[roomID]) == 0 {
			delete(m.states, roomID)
		}
	}
}

func (m *VoiceStateManager) UpdateState(roomID, userID string, muted, videoEnabled, speaking bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.states[roomID] != nil && m.states[roomID][userID] != nil {
		state := m.states[roomID][userID]
		state.Muted = muted
		state.VideoEnabled = videoEnabled
		state.Speaking = speaking
	}
}

func (m *VoiceStateManager) GetState(roomID, userID string) *VoiceState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.states[roomID] != nil {
		return m.states[roomID][userID]
	}
	return nil
}

func (m *VoiceStateManager) GetRoomStates(roomID string) []*VoiceState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make([]*VoiceState, 0)
	if m.states[roomID] != nil {
		for _, state := range m.states[roomID] {
			states = append(states, state)
		}
	}
	return states
}

func NewHandler(voiceAssign *voiceassign.Service, roomsRepo *rooms.Repository, hub *events.Hub, logger *zap.Logger) *Handler {
	return &Handler{
		voiceAssign: voiceAssign,
		roomsRepo:   roomsRepo,
		voiceStates: NewVoiceStateManager(),
		hub:         hub,
		logger:      logger,
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

	assignment, err := h.voiceAssign.AssignVoiceServer(ctx, userID, req.RoomId)
	if err != nil {
		h.logger.Error("failed to assign voice server", zap.Error(err))
		return nil, errors.ToGRPCError(errors.Internal("failed to assign voice server", err))
	}

	h.voiceStates.AddUser(req.RoomId, userID, assignment.ServerID, req.AudioOnly)

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

	existingStates := h.voiceStates.GetRoomStates(req.RoomId)
	participants := make([]*callv1.Participant, 0, len(existingStates))
	for _, state := range existingStates {
		if state.UserID != userID {
			participants = append(participants, &callv1.Participant{
				UserId:       state.UserID,
				Ssrc:         0,
				Muted:        state.Muted,
				VideoEnabled: state.VideoEnabled,
			})
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

func (h *Handler) LeaveVoice(ctx context.Context, req *callv1.LeaveVoiceRequest) (*callv1.EmptyResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	h.voiceStates.RemoveUser(req.RoomId, userID)

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

	return &callv1.EmptyResponse{}, nil
}

func (h *Handler) SetMediaPrefs(ctx context.Context, req *callv1.SetMediaPrefsRequest) (*callv1.EmptyResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	state := h.voiceStates.GetState(req.RoomId, userID)
	if state == nil {
		return nil, errors.ToGRPCError(errors.NotFound("user not in voice channel"))
	}

	h.voiceStates.UpdateState(req.RoomId, userID, req.Muted, req.VideoEnabled, false)

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

	return &callv1.EmptyResponse{}, nil
}

func (h *Handler) GetVoiceStatus(ctx context.Context, req *callv1.GetVoiceStatusRequest) (*callv1.GetVoiceStatusResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	states := h.voiceStates.GetRoomStates(req.RoomId)
	participants := make([]*callv1.VoiceParticipant, len(states))
	for i, state := range states {
		participants[i] = &callv1.VoiceParticipant{
			UserId:       state.UserID,
			Muted:        state.Muted,
			VideoEnabled: state.VideoEnabled,
			Speaking:     state.Speaking,
			JoinedAt:     &state.JoinedAt,
		}
	}

	return &callv1.GetVoiceStatusResponse{
		Participants:      participants,
		TotalParticipants: int32(len(participants)),
	}, nil
}
