package udp

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"

	voiceauth "github.com/Alexander-D-Karpov/concord/internal/voice/auth"
	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	"github.com/Alexander-D-Karpov/concord/internal/voice/router"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

type Handler struct {
	sessionManager *session.Manager
	router         *router.Router
	validator      *voiceauth.Validator
	logger         *zap.Logger
	metrics        *telemetry.Metrics
}

func NewHandler(
	sessionManager *session.Manager,
	voiceRouter *router.Router,
	validator *voiceauth.Validator,
	logger *zap.Logger,
	metrics *telemetry.Metrics,
) *Handler {
	return &Handler{
		sessionManager: sessionManager,
		router:         voiceRouter,
		validator:      validator,
		logger:         logger,
		metrics:        metrics,
	}
}

func (h *Handler) HandlePacket(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < 1 {
		return
	}

	if h.metrics != nil {
		h.metrics.RecordPacketReceived(uint64(len(data)))
	}

	switch data[0] {
	case protocol.PacketTypeHello:
		h.handleHello(data, addr, conn)
	case protocol.PacketTypeAudio, protocol.PacketTypeVideo:
		h.handleMedia(data, addr, conn)
	case protocol.PacketTypePing:
		h.handlePing(data, addr, conn)
	case protocol.PacketTypeBye:
		h.handleBye(data, addr, conn)
	case protocol.PacketTypeSpeaking:
		h.handleSpeaking(data, addr, conn)
	case protocol.PacketTypeMediaState:
		h.handleMediaState(data, addr, conn)
	case protocol.PacketTypeNack:
		h.handleNack(data, addr, conn)
	case protocol.PacketTypePli:
		h.handlePli(data, addr, conn)
	case protocol.PacketTypeRR:
		h.handleReceiverReport(data, addr, conn)
	case protocol.PacketTypeSubscribe:
		h.handleSubscribe(data, addr, conn)
	case protocol.PacketTypeQualityReport:
		h.handleQualityReport(data, addr, conn)
	}
}

func (h *Handler) handleMedia(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < protocol.MediaHeaderSize {
		return
	}

	pkt, err := protocol.ParsePacket(data)
	if err != nil {
		return
	}

	sess := h.sessionManager.GetBySSRC(pkt.Header.SSRC)
	if sess == nil {
		return
	}

	h.sessionManager.BindAddr(sess.ID, addr)
	h.sessionManager.Touch(sess.ID)
	sess.StoreForRetransmit(pkt.Header.Sequence, data)
	h.router.RouteMediaRaw(pkt.Header, data, addr, conn)
}

func (h *Handler) handleHello(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var hello protocol.HelloPayload
	if err := json.Unmarshal(data[1:], &hello); err != nil {
		h.logger.Error("failed to unmarshal hello", zap.Error(err))
		return
	}

	claims, err := h.validator.ValidateToken(context.Background(), hello.Token)
	if err != nil {
		h.logger.Warn("invalid token in hello", zap.Error(err))
		return
	}

	var sessionCrypto *crypto.SessionCrypto
	if hello.Crypto != nil && len(hello.Crypto.KeyMaterial) == crypto.KeySize {
		var keyID uint8
		if len(hello.Crypto.KeyID) > 0 {
			keyID = hello.Crypto.KeyID[0]
		}
		sessionCrypto, _ = crypto.NewSessionCrypto(hello.Crypto.KeyMaterial, hello.Crypto.NonceBase, keyID)
	}

	existing := h.sessionManager.GetSessionByUserInRoom(claims.UserID, claims.RoomID)
	if existing != nil {
		h.broadcastParticipantLeft(existing.RoomID, existing.UserID, existing.SSRC, existing.VideoSSRC, conn)
		h.sessionManager.RemoveSession(existing.ID)
	}

	existingSessions := h.sessionManager.GetRoomSessions(claims.RoomID)
	sess := h.sessionManager.CreateSession(claims.UserID, claims.RoomID, addr, sessionCrypto, hello.VideoEnabled)

	participants := make([]protocol.ParticipantInfo, 0, len(existingSessions))
	for _, es := range existingSessions {
		participants = append(participants, protocol.ParticipantInfo{
			UserID:        es.UserID,
			SSRC:          es.SSRC,
			VideoSSRC:     es.VideoSSRC,
			ScreenSSRC:    es.ScreenSSRC,
			Muted:         es.Muted,
			VideoEnabled:  es.VideoEnabled,
			ScreenSharing: es.ScreenSharing,
		})
	}

	welcome := protocol.WelcomePayload{
		SessionID:    sess.ID,
		SSRC:         sess.SSRC,
		VideoSSRC:    sess.VideoSSRC,
		ScreenSSRC:   sess.ScreenSSRC,
		Participants: participants,
	}

	welcomeData, _ := json.Marshal(welcome)
	out := make([]byte, 1+len(welcomeData))
	out[0] = protocol.PacketTypeWelcome
	copy(out[1:], welcomeData)
	_ = h.send(out, addr, conn)

	h.broadcastJoined(claims.RoomID, sess, conn)

	h.logger.Info("session created",
		zap.String("user_id", claims.UserID),
		zap.String("room_id", claims.RoomID),
		zap.Uint32("ssrc", sess.SSRC),
		zap.Uint32("video_ssrc", sess.VideoSSRC),
		zap.Uint32("screen_ssrc", sess.ScreenSSRC),
	)
}

func (h *Handler) handlePing(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if sess := h.sessionManager.GetByAddr(addr); sess != nil {
		h.sessionManager.Touch(sess.ID)
	}

	pong := make([]byte, len(data))
	pong[0] = protocol.PacketTypePong
	copy(pong[1:], data[1:])
	_ = h.send(pong, addr, conn)
}

func (h *Handler) handleBye(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < 5 {
		return
	}

	ssrc := binary.BigEndian.Uint32(data[1:5])
	sess := h.sessionManager.GetBySSRC(ssrc)
	if sess == nil {
		return
	}

	roomID := sess.RoomID
	userID := sess.UserID
	videoSSRC := sess.VideoSSRC

	h.sessionManager.RemoveSession(sess.ID)
	h.broadcastParticipantLeft(roomID, userID, ssrc, videoSSRC, conn)

	h.logger.Info("session ended",
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
	)
}

func (h *Handler) handleSpeaking(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var speaking protocol.SpeakingPayload
	if err := json.Unmarshal(data[1:], &speaking); err != nil {
		return
	}

	sess := h.sessionManager.GetBySSRC(speaking.SSRC)
	if sess == nil {
		return
	}

	sess.SetSpeaking(speaking.Speaking)
	h.sessionManager.Touch(sess.ID)

	sessions := h.sessionManager.GetRoomSessions(sess.RoomID)
	payload, _ := json.Marshal(speaking)
	pkt := make([]byte, 1+len(payload))
	pkt[0] = protocol.PacketTypeSpeaking
	copy(pkt[1:], payload)

	for _, s := range sessions {
		if s.SSRC == speaking.SSRC {
			continue
		}
		if to := s.GetAddr(); to != nil {
			_ = h.send(pkt, to, conn)
		}
	}
}

func (h *Handler) handleMediaState(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var ms protocol.MediaStatePayload
	if err := json.Unmarshal(data[1:], &ms); err != nil {
		return
	}

	sess := h.sessionManager.GetBySSRC(ms.SSRC)
	if sess == nil {
		return
	}

	sess.SetMuted(ms.Muted)
	sess.SetVideoEnabled(ms.VideoEnabled)
	sess.SetScreenSharing(ms.ScreenSharing)
	h.sessionManager.Touch(sess.ID)
	h.broadcastMediaState(sess.RoomID, sess, conn)
}

func (h *Handler) handleNack(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	nack, err := protocol.ParseNack(data)
	if err != nil {
		return
	}

	target := h.sessionManager.GetBySSRC(nack.SSRC)
	if target == nil {
		return
	}

	for _, seq := range nack.Sequences {
		if cached := target.GetForRetransmit(seq); cached != nil {
			_ = h.send(cached, addr, conn)
		}
	}
}

func (h *Handler) handlePli(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	pli, err := protocol.ParsePli(data)
	if err != nil {
		return
	}

	target := h.sessionManager.GetBySSRC(pli.SSRC)
	if target == nil {
		return
	}

	if to := target.GetAddr(); to != nil {
		_ = h.send(data, to, conn)
	}
}

func (h *Handler) handleReceiverReport(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	rr, err := protocol.ParseReceiverReport(data)
	if err != nil {
		return
	}

	target := h.sessionManager.GetBySSRC(rr.SSRC)
	if target == nil {
		return
	}

	if to := target.GetAddr(); to != nil {
		_ = h.send(data, to, conn)
	}
}

func (h *Handler) handleSubscribe(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	sess := h.sessionManager.GetByAddr(addr)
	if sess == nil {
		return
	}

	var payload protocol.SubscribePayload
	if err := json.Unmarshal(data[1:], &payload); err != nil {
		return
	}

	sess.UpdateSubscriptions(payload.Subscriptions)
}

func (h *Handler) handleQualityReport(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < 2 {
		return
	}

	sess := h.sessionManager.GetByAddr(addr)
	if sess == nil {
		return
	}

	h.sessionManager.Touch(sess.ID)

	sessions := h.sessionManager.GetRoomSessions(sess.RoomID)
	for _, other := range sessions {
		if other.ID == sess.ID {
			continue
		}
		if to := other.GetAddr(); to != nil {
			_ = h.send(data, to, conn)
		}
	}
}

func (h *Handler) broadcastJoined(roomID string, newSess *session.Session, conn *net.UDPConn) {
	sessions := h.sessionManager.GetRoomSessions(roomID)

	payload := protocol.MediaStatePayload{
		SSRC: newSess.SSRC, VideoSSRC: newSess.VideoSSRC, ScreenSSRC: newSess.ScreenSSRC,
		UserID: newSess.UserID, RoomID: roomID,
		Muted: newSess.Muted, VideoEnabled: newSess.VideoEnabled, ScreenSharing: newSess.ScreenSharing,
	}
	data, _ := json.Marshal(payload)
	pkt := make([]byte, 1+len(data))
	pkt[0] = protocol.PacketTypeMediaState
	copy(pkt[1:], data)

	for _, s := range sessions {
		if s.ID == newSess.ID {
			continue
		}
		if to := s.GetAddr(); to != nil {
			_ = h.send(pkt, to, conn)
		}
	}
}

func (h *Handler) broadcastParticipantLeft(roomID, userID string, ssrc, videoSSRC uint32, conn *net.UDPConn) {
	sessions := h.sessionManager.GetRoomSessions(roomID)

	payload := protocol.ParticipantLeftPayload{UserID: userID, RoomID: roomID, SSRC: ssrc, VideoSSRC: videoSSRC}
	data, _ := json.Marshal(payload)
	pkt := make([]byte, 1+len(data))
	pkt[0] = protocol.PacketTypeParticipantLeft
	copy(pkt[1:], data)

	for _, s := range sessions {
		if to := s.GetAddr(); to != nil {
			_ = h.send(pkt, to, conn)
		}
	}
}

func (h *Handler) broadcastMediaState(roomID string, sess *session.Session, conn *net.UDPConn) {
	sessions := h.sessionManager.GetRoomSessions(roomID)

	payload := protocol.MediaStatePayload{
		SSRC: sess.SSRC, VideoSSRC: sess.VideoSSRC, ScreenSSRC: sess.ScreenSSRC,
		UserID: sess.UserID, RoomID: roomID,
		Muted: sess.Muted, VideoEnabled: sess.VideoEnabled, ScreenSharing: sess.ScreenSharing,
	}
	data, _ := json.Marshal(payload)
	pkt := make([]byte, 1+len(data))
	pkt[0] = protocol.PacketTypeMediaState
	copy(pkt[1:], data)

	for _, s := range sessions {
		if s.ID == sess.ID {
			continue
		}
		if to := s.GetAddr(); to != nil {
			_ = h.send(pkt, to, conn)
		}
	}
}

func (h *Handler) send(data []byte, addr *net.UDPAddr, conn *net.UDPConn) error {
	_, err := conn.WriteToUDP(data, addr)
	if err == nil && h.metrics != nil {
		h.metrics.RecordPacketSent(uint64(len(data)))
	}
	return err
}
