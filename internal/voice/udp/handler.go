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

func NewHandler(sessionManager *session.Manager, voiceRouter *router.Router, validator *voiceauth.Validator, logger *zap.Logger, metrics *telemetry.Metrics) *Handler {
	return &Handler{sessionManager: sessionManager, router: voiceRouter, validator: validator, logger: logger, metrics: metrics}
}

func (h *Handler) handlePacket(data []byte, owner router.PacketOwner, addr *net.UDPAddr, conn *net.UDPConn) {
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
		h.handleMedia(data, owner, addr, conn)
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

func (h *Handler) HandlePacket(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	h.handlePacket(data, nil, addr, conn)
}

func (h *Handler) HandlePacketOwned(data []byte, owner router.PacketOwner, addr *net.UDPAddr, conn *net.UDPConn) {
	h.handlePacket(data, owner, addr, conn)
}

func (h *Handler) handleMedia(data []byte, owner router.PacketOwner, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < protocol.MediaHeaderSize {
		return
	}
	hdr, err := protocol.ParseMediaHeader(data)
	if err != nil {
		return
	}
	sess := h.sessionManager.GetBySSRC(hdr.SSRC)
	if sess == nil {
		return
	}
	if sess.AddrChanged(addr) {
		h.sessionManager.BindAddr(sess.ID, addr)
	}
	h.sessionManager.Touch(sess.ID)
	if h.metrics != nil {
		if hdr.Type == protocol.PacketTypeAudio {
			h.metrics.RecordAudioIn()
		} else {
			h.metrics.RecordVideoIn()
		}
	}
	if hdr.Type == protocol.PacketTypeVideo {
		sess.StoreForRetransmit(hdr.SSRC, hdr.Sequence, data)
	}
	if owner != nil {
		h.router.RouteMediaOwned(*hdr, data, owner, addr, conn)
	} else {
		h.router.RouteMediaRaw(*hdr, data, addr, conn)
	}
}

func (h *Handler) handleHello(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var hello protocol.HelloPayload
	if err := json.Unmarshal(data[1:], &hello); err != nil {
		h.logger.Warn("failed to unmarshal hello", zap.Error(err))
		return
	}
	if h.metrics != nil {
		h.metrics.RecordHello()
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

	if existing := h.sessionManager.GetSessionByUserInRoom(claims.UserID, claims.RoomID); existing != nil {
		h.sessionManager.RemoveSession(existing.ID)
		h.broadcastParticipantLeft(claims.RoomID, existing.UserID, existing.SSRC, existing.VideoSSRC, existing.ScreenSSRC, conn)
	}

	existingSessions := h.sessionManager.GetRoomSessions(claims.RoomID)
	sess := h.sessionManager.CreateSession(claims.UserID, claims.RoomID, addr, sessionCrypto, hello.VideoEnabled)

	participants := make([]protocol.ParticipantInfo, 0, len(existingSessions))
	for _, s := range existingSessions {
		if s == nil {
			continue
		}
		quality, rttMs, packetLoss, jitterMs := s.SnapshotQuality()
		participants = append(participants, protocol.ParticipantInfo{
			UserID:        s.UserID,
			SSRC:          s.SSRC,
			VideoSSRC:     s.VideoSSRC,
			ScreenSSRC:    s.ScreenSSRC,
			Muted:         s.Muted,
			VideoEnabled:  s.VideoEnabled,
			ScreenSharing: s.ScreenSharing,
			Speaking:      s.Speaking,
			Quality:       quality,
			RTTMs:         rttMs,
			PacketLoss:    packetLoss,
			JitterMs:      jitterMs,
		})
	}

	welcome := protocol.WelcomePayload{
		Protocol:       protocol.ProtocolVersion,
		SessionID:      sess.ID,
		RoomID:         claims.RoomID,
		UserID:         claims.UserID,
		SSRC:           sess.SSRC,
		VideoSSRC:      sess.VideoSSRC,
		ScreenSSRC:     sess.ScreenSSRC,
		PingIntervalMs: 5000,
		RRIntervalMs:   250,
		Participants:   participants,
	}
	out, err := protocol.BuildJSONPacket(protocol.PacketTypeWelcome, welcome)
	if err != nil {
		h.logger.Warn("failed to marshal welcome", zap.Error(err))
		return
	}
	if err := h.send(out, addr, conn); err != nil {
		h.logger.Warn("failed to send welcome", zap.Error(err))
		return
	}
	if h.metrics != nil {
		h.metrics.RecordWelcome()
	}

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
	if h.metrics != nil {
		h.metrics.RecordPing()
	}
	if sess := h.sessionManager.GetByAddr(addr); sess != nil {
		h.sessionManager.Touch(sess.ID)
	}
	pong := make([]byte, len(data))
	pong[0] = protocol.PacketTypePong
	copy(pong[1:], data[1:])
	_ = h.send(pong, addr, conn)
	if h.metrics != nil {
		h.metrics.RecordPong()
	}
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
	if h.metrics != nil {
		h.metrics.RecordBye()
	}
	roomID := sess.RoomID
	userID := sess.UserID
	videoSSRC := sess.VideoSSRC
	screenSSRC := sess.ScreenSSRC
	h.sessionManager.RemoveSession(sess.ID)
	h.broadcastParticipantLeft(roomID, userID, ssrc, videoSSRC, screenSSRC, conn)
	h.logger.Info("session ended", zap.String("user_id", userID), zap.String("room_id", roomID))
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
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeSpeaking, protocol.SpeakingPayload{
		SSRC:      sess.SSRC,
		VideoSSRC: sess.VideoSSRC,
		UserID:    sess.UserID,
		RoomID:    sess.RoomID,
		Speaking:  speaking.Speaking,
	})
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, sess.RoomID, sess.ID)
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
	if h.metrics != nil {
		h.metrics.RecordNack()
	}
	target := h.sessionManager.GetBySSRC(nack.SSRC)
	if target == nil {
		return
	}
	for _, seq := range nack.Sequences {
		if cached := target.GetForRetransmit(nack.SSRC, seq); cached != nil {
			if err := h.send(cached, addr, conn); err == nil && h.metrics != nil {
				h.metrics.RecordRetransmit()
			}
		}
	}
}

func (h *Handler) handlePli(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	pli, err := protocol.ParsePli(data)
	if err != nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordPli()
	}
	target := h.sessionManager.GetBySSRC(pli.SSRC)
	if target == nil {
		return
	}
	_ = h.router.RouteControlToSession(data, conn, target)
}

func (h *Handler) handleReceiverReport(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	rr, err := protocol.ParseReceiverReport(data)
	if err != nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordReceiverReport()
	}
	target := h.sessionManager.GetBySSRC(rr.SSRC)
	if target == nil {
		return
	}
	_ = h.router.RouteControlToSession(data, conn, target)
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
	if h.metrics != nil {
		h.metrics.RecordSubscribe()
	}
	h.sessionManager.Touch(sess.ID)
	sess.UpdateSubscriptions(payload.Subscriptions)
}

func (h *Handler) handleQualityReport(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var payload protocol.QualityReportPayload
	if err := json.Unmarshal(data[1:], &payload); err != nil {
		return
	}
	sess := h.sessionManager.GetByAddr(addr)
	if sess == nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordQualityReport()
		if payload.RTTMs > 0 {
			h.metrics.RecordRTT(payload.RTTMs)
		}
	}
	payload.UserID = sess.UserID
	payload.RoomID = sess.RoomID
	payload.SSRC = sess.SSRC
	sess.SetQuality(payload.Quality, payload.RTTMs, payload.PacketLoss, payload.JitterMs)
	h.sessionManager.Touch(sess.ID)
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeQualityReport, payload)
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, sess.RoomID, sess.ID)
}

func (h *Handler) broadcastJoined(roomID string, newSess *session.Session, conn *net.UDPConn) {
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeMediaState, protocol.MediaStatePayload{
		SSRC:          newSess.SSRC,
		VideoSSRC:     newSess.VideoSSRC,
		ScreenSSRC:    newSess.ScreenSSRC,
		UserID:        newSess.UserID,
		RoomID:        roomID,
		Muted:         newSess.Muted,
		VideoEnabled:  newSess.VideoEnabled,
		ScreenSharing: newSess.ScreenSharing,
	})
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, roomID, newSess.ID)
}

func (h *Handler) broadcastParticipantLeft(roomID, userID string, ssrc, videoSSRC, screenSSRC uint32, conn *net.UDPConn) {
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeParticipantLeft, protocol.ParticipantLeftPayload{
		UserID:     userID,
		RoomID:     roomID,
		SSRC:       ssrc,
		VideoSSRC:  videoSSRC,
		ScreenSSRC: screenSSRC,
	})
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, roomID, 0)
}

func (h *Handler) broadcastMediaState(roomID string, sess *session.Session, conn *net.UDPConn) {
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeMediaState, protocol.MediaStatePayload{
		SSRC:          sess.SSRC,
		VideoSSRC:     sess.VideoSSRC,
		ScreenSSRC:    sess.ScreenSSRC,
		UserID:        sess.UserID,
		RoomID:        roomID,
		Muted:         sess.Muted,
		VideoEnabled:  sess.VideoEnabled,
		ScreenSharing: sess.ScreenSharing,
	})
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, roomID, sess.ID)
}

func (h *Handler) send(data []byte, addr *net.UDPAddr, conn *net.UDPConn) error {
	_, err := conn.WriteToUDP(data, addr)
	if err == nil && h.metrics != nil {
		h.metrics.RecordPacketSent(uint64(len(data)))
	}
	return err
}
