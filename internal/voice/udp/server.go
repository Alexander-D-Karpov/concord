package udp

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	voiceauth "github.com/Alexander-D-Karpov/concord/internal/voice/auth"
	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	"github.com/Alexander-D-Karpov/concord/internal/voice/router"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

type Server struct {
	conn           *net.UDPConn
	sessionManager *session.Manager
	router         *router.Router
	validator      *voiceauth.Validator
	logger         *zap.Logger
	metrics        *telemetry.Metrics
	stopChan       chan struct{}
	wg             sync.WaitGroup

	packetPool sync.Pool
	workChan   chan *packetJob
}

type packetJob struct {
	data []byte
	addr *net.UDPAddr
}

const (
	numWorkers   = 4
	workChanSize = 10000
	maxPacketLen = 1500
)

func NewServer(
	host string,
	port int,
	sessionManager *session.Manager,
	router *router.Router,
	jwtManager *jwt.Manager,
	logger *zap.Logger,
	metrics *telemetry.Metrics,
) (*Server, error) {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return nil, fmt.Errorf("resolve UDP address: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen UDP: %w", err)
	}

	if err := conn.SetReadBuffer(8 * 1024 * 1024); err != nil {
		logger.Warn("failed to set read buffer", zap.Error(err))
	}
	if err := conn.SetWriteBuffer(8 * 1024 * 1024); err != nil {
		logger.Warn("failed to set write buffer", zap.Error(err))
	}

	validator := voiceauth.NewValidator(jwtManager)

	return &Server{
		conn:           conn,
		sessionManager: sessionManager,
		router:         router,
		validator:      validator,
		logger:         logger,
		metrics:        metrics,
		stopChan:       make(chan struct{}),
		workChan:       make(chan *packetJob, workChanSize),
		packetPool: sync.Pool{
			New: func() interface{} {
				b := make([]byte, maxPacketLen)
				return &b
			},
		},
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	s.logger.Info("UDP server starting", zap.String("address", s.conn.LocalAddr().String()))

	for i := 0; i < numWorkers; i++ {
		s.wg.Add(1)
		go s.worker()
	}

	s.wg.Add(1)
	go s.readLoop()

	<-ctx.Done()
	close(s.stopChan)

	if err := s.conn.Close(); err != nil {
		return err
	}

	s.wg.Wait()
	s.logger.Info("UDP server stopped")
	return nil
}

func (s *Server) readLoop() {
	defer s.wg.Done()

	for {
		bufPtr := s.packetPool.Get().(*[]byte)
		buf := *bufPtr

		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			s.packetPool.Put(bufPtr)
			select {
			case <-s.stopChan:
				return
			default:
				s.logger.Error("read UDP packet error", zap.Error(err))
				continue
			}
		}

		if n > maxPacketLen {
			s.packetPool.Put(bufPtr)
			s.logger.Warn("packet too large", zap.Int("size", n))
			continue
		}

		packetData := make([]byte, n)
		copy(packetData, buf[:n])
		s.packetPool.Put(bufPtr)

		select {
		case s.workChan <- &packetJob{data: packetData, addr: addr}:
		default:
			if s.metrics != nil {
				s.metrics.RecordPacketDropped()
			}
		}
	}
}

func (s *Server) worker() {
	defer s.wg.Done()

	for {
		select {
		case job := <-s.workChan:
			if job != nil {
				s.handlePacket(job.data, job.addr)
			}
		case <-s.stopChan:
			return
		}
	}
}

func (s *Server) handlePacket(data []byte, addr *net.UDPAddr) {
	if len(data) < 1 {
		return
	}

	if s.metrics != nil {
		s.metrics.RecordPacketReceived(uint64(len(data)))
	}

	switch data[0] {
	case protocol.PacketTypeHello:
		s.handleHello(data, addr)
	case protocol.PacketTypeAudio, protocol.PacketTypeVideo:
		s.handleMedia(data, addr)
	case protocol.PacketTypePing:
		s.handlePing(data, addr)
	case protocol.PacketTypeBye:
		s.handleBye(data, addr)
	case protocol.PacketTypeSpeaking:
		s.handleSpeaking(data, addr)
	case protocol.PacketTypeMediaState:
		s.handleMediaState(data, addr)
	case protocol.PacketTypeNack:
		s.handleNack(data, addr)
	case protocol.PacketTypePli:
		s.handlePli(data, addr)
	case protocol.PacketTypeRR:
		s.handleReceiverReport(data, addr)
	case protocol.PacketTypeSubscribe:
		s.handleSubscribe(data, addr)
	default:
		s.logger.Debug("unknown packet type", zap.Uint8("type", data[0]))
	}
}

func (s *Server) handleSubscribe(data []byte, addr *net.UDPAddr) {
	sess := s.sessionManager.GetByAddr(addr)
	if sess == nil {
		return
	}

	var payload protocol.SubscribePayload
	if err := json.Unmarshal(data[1:], &payload); err != nil {
		return
	}

	sess.UpdateSubscriptions(payload.Subscriptions)
}

func (s *Server) handleMedia(data []byte, addr *net.UDPAddr) {
	if len(data) < protocol.MediaHeaderSize {
		s.logger.Debug("media packet too small", zap.Int("size", len(data)))
		return
	}

	pkt, err := protocol.ParsePacket(data)
	if err != nil {
		s.logger.Debug("failed to parse packet", zap.Error(err))
		return
	}

	// Route by SSRC (header is plaintext)
	sess := s.sessionManager.GetBySSRC(pkt.Header.SSRC)
	if sess == nil {
		s.logger.Debug("unknown SSRC", zap.Uint32("ssrc", pkt.Header.SSRC))
		return
	}

	// NAT rebinding: keep addrMap updated
	s.sessionManager.BindAddr(sess.ID, addr)
	s.sessionManager.Touch(sess.ID)

	// IMPORTANT: store the on-the-wire packet bytes for NACK retransmit
	sess.StoreForRetransmit(pkt.Header.Sequence, data)

	// IMPORTANT: forward raw bytes (ciphertext) untouched
	s.router.RouteMediaRaw(pkt.Header, data, addr, s.conn)
}

func (s *Server) handleHello(data []byte, addr *net.UDPAddr) {
	var hello protocol.HelloPayload
	if err := json.Unmarshal(data[1:], &hello); err != nil {
		s.logger.Error("failed to unmarshal hello", zap.Error(err))
		return
	}

	claims, err := s.validator.ValidateToken(context.Background(), hello.Token)
	if err != nil {
		s.logger.Warn("invalid token in hello", zap.Error(err))
		return
	}

	var sessionCrypto *crypto.SessionCrypto
	if hello.Crypto != nil && len(hello.Crypto.KeyMaterial) == crypto.KeySize {
		var keyID uint8
		if len(hello.Crypto.KeyID) > 0 {
			keyID = hello.Crypto.KeyID[0]
		}

		sessionCrypto, err = crypto.NewSessionCrypto(
			hello.Crypto.KeyMaterial,
			hello.Crypto.NonceBase,
			keyID,
		)
		if err != nil {
			s.logger.Error("failed to create session crypto", zap.Error(err))
			return
		}
	}

	existingSession := s.sessionManager.GetSessionByUserInRoom(claims.UserID, claims.RoomID)
	if existingSession != nil {
		s.broadcastParticipantLeft(existingSession.RoomID, existingSession.UserID, existingSession.SSRC, existingSession.VideoSSRC)
		s.sessionManager.RemoveSession(existingSession.ID)
	}

	existingSessions := s.sessionManager.GetRoomSessions(claims.RoomID)

	sess := s.sessionManager.CreateSession(claims.UserID, claims.RoomID, addr, sessionCrypto, hello.VideoEnabled)

	participants := make([]protocol.ParticipantInfo, 0, len(existingSessions))
	for _, existing := range existingSessions {
		participants = append(participants, protocol.ParticipantInfo{
			UserID:        existing.UserID,
			SSRC:          existing.SSRC,
			VideoSSRC:     existing.VideoSSRC,
			ScreenSSRC:    existing.ScreenSSRC,
			Muted:         existing.Muted,
			VideoEnabled:  existing.VideoEnabled,
			ScreenSharing: existing.ScreenSharing,
		})
	}

	welcome := protocol.WelcomePayload{
		SessionID:    sess.ID,
		SSRC:         sess.SSRC,
		VideoSSRC:    sess.VideoSSRC,
		ScreenSSRC:   sess.ScreenSSRC,
		Participants: participants,
	}

	welcomeData, err := json.Marshal(welcome)
	if err != nil {
		s.logger.Error("failed to marshal welcome", zap.Error(err))
		return
	}

	out := make([]byte, 1+len(welcomeData))
	out[0] = protocol.PacketTypeWelcome
	copy(out[1:], welcomeData)

	if err := s.send(out, addr); err != nil {
		s.logger.Error("failed to send welcome", zap.Error(err))
		return
	}

	s.broadcastParticipantJoined(claims.RoomID, sess)

	s.logger.Info("session created",
		zap.String("user_id", claims.UserID),
		zap.String("room_id", claims.RoomID),
		zap.Uint32("ssrc", sess.SSRC),
		zap.Uint32("video_ssrc", sess.VideoSSRC),
		zap.Uint32("screen_ssrc", sess.ScreenSSRC),
	)
}

func (s *Server) handlePing(data []byte, addr *net.UDPAddr) {
	if sess := s.sessionManager.GetByAddr(addr); sess != nil {
		s.sessionManager.Touch(sess.ID)
	}

	pong := make([]byte, len(data))
	pong[0] = protocol.PacketTypePong
	copy(pong[1:], data[1:])

	if err := s.send(pong, addr); err != nil {
		s.logger.Error("failed to send pong", zap.Error(err))
	}
}

func (s *Server) handleBye(data []byte, addr *net.UDPAddr) {
	if len(data) < 5 {
		return
	}

	ssrc := binary.BigEndian.Uint32(data[1:5])

	sess := s.sessionManager.GetBySSRC(ssrc)
	if sess == nil {
		return
	}

	roomID := sess.RoomID
	userID := sess.UserID
	videoSSRC := sess.VideoSSRC

	s.sessionManager.RemoveSession(sess.ID)
	s.broadcastParticipantLeft(roomID, userID, ssrc, videoSSRC)

	s.logger.Info("session ended",
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
	)
}

func (s *Server) handleSpeaking(data []byte, addr *net.UDPAddr) {
	var speaking protocol.SpeakingPayload
	if err := json.Unmarshal(data[1:], &speaking); err != nil {
		return
	}

	sess := s.sessionManager.GetBySSRC(speaking.SSRC)
	if sess == nil {
		return
	}

	sess.SetSpeaking(speaking.Speaking)
	s.sessionManager.Touch(sess.ID)

	s.broadcastSpeaking(sess.RoomID, speaking)
}

func (s *Server) handleMediaState(data []byte, addr *net.UDPAddr) {
	var mediaState protocol.MediaStatePayload
	if err := json.Unmarshal(data[1:], &mediaState); err != nil {
		return
	}

	sess := s.sessionManager.GetBySSRC(mediaState.SSRC)
	if sess == nil {
		return
	}

	sess.SetMuted(mediaState.Muted)
	sess.SetVideoEnabled(mediaState.VideoEnabled)
	sess.SetScreenSharing(mediaState.ScreenSharing)
	s.sessionManager.Touch(sess.ID)

	s.broadcastMediaState(sess.RoomID, sess)
}

func (s *Server) handleNack(data []byte, addr *net.UDPAddr) {
	nack, err := protocol.ParseNack(data)
	if err != nil {
		return
	}

	targetSess := s.sessionManager.GetBySSRC(nack.SSRC)
	if targetSess == nil {
		return
	}

	for _, seq := range nack.Sequences {
		if cached := targetSess.GetForRetransmit(seq); cached != nil {
			_ = s.send(cached, addr) // addr = requester
		}
	}
}

func (s *Server) handlePli(data []byte, addr *net.UDPAddr) {
	pli, err := protocol.ParsePli(data)
	if err != nil {
		return
	}

	targetSess := s.sessionManager.GetBySSRC(pli.SSRC)
	if targetSess == nil {
		return
	}

	to := targetSess.GetAddr()
	if to == nil {
		return
	}

	_ = s.send(data, to)
}

func (s *Server) handleReceiverReport(data []byte, addr *net.UDPAddr) {
	rr, err := protocol.ParseReceiverReport(data)
	if err != nil {
		return
	}

	targetSess := s.sessionManager.GetBySSRC(rr.SSRC)
	if targetSess == nil {
		return
	}

	to := targetSess.GetAddr()
	if to == nil {
		return
	}

	_ = s.send(data, to)
}

func (s *Server) broadcastParticipantJoined(roomID string, newSession *session.Session) {
	sessions := s.sessionManager.GetRoomSessions(roomID)

	payload := protocol.MediaStatePayload{
		SSRC:          newSession.SSRC,
		VideoSSRC:     newSession.VideoSSRC,
		ScreenSSRC:    newSession.ScreenSSRC,
		UserID:        newSession.UserID,
		RoomID:        roomID,
		Muted:         newSession.Muted,
		VideoEnabled:  newSession.VideoEnabled,
		ScreenSharing: newSession.ScreenSharing,
	}

	payloadData, _ := json.Marshal(payload)
	packet := make([]byte, 1+len(payloadData))
	packet[0] = protocol.PacketTypeMediaState
	copy(packet[1:], payloadData)

	for _, sess := range sessions {
		if sess.ID == newSession.ID {
			continue
		}
		to := sess.GetAddr()
		if to == nil {
			continue
		}
		_ = s.send(packet, to)
	}
}

func (s *Server) broadcastParticipantLeft(roomID, userID string, ssrc uint32, videoSSRC uint32) {
	sessions := s.sessionManager.GetRoomSessions(roomID)

	payload := protocol.ParticipantLeftPayload{
		UserID:    userID,
		RoomID:    roomID,
		SSRC:      ssrc,
		VideoSSRC: videoSSRC,
	}

	b, _ := json.Marshal(payload)
	pkt := make([]byte, 1+len(b))
	pkt[0] = protocol.PacketTypeParticipantLeft
	copy(pkt[1:], b)

	for _, sess := range sessions {
		to := sess.GetAddr()
		if to == nil {
			continue
		}
		_ = s.send(pkt, to)
	}
}

func (s *Server) broadcastSpeaking(roomID string, speaking protocol.SpeakingPayload) {
	sessions := s.sessionManager.GetRoomSessions(roomID)

	payload, _ := json.Marshal(speaking)
	packet := make([]byte, 1+len(payload))
	packet[0] = protocol.PacketTypeSpeaking
	copy(packet[1:], payload)

	for _, sess := range sessions {
		if sess.SSRC == speaking.SSRC {
			continue
		}
		to := sess.GetAddr()
		if to == nil {
			continue
		}
		_ = s.send(packet, to)
	}
}

func (s *Server) broadcastMediaState(roomID string, sess *session.Session) {
	sessions := s.sessionManager.GetRoomSessions(roomID)

	payload := protocol.MediaStatePayload{
		SSRC:          sess.SSRC,
		VideoSSRC:     sess.VideoSSRC,
		ScreenSSRC:    sess.ScreenSSRC,
		UserID:        sess.UserID,
		RoomID:        roomID,
		Muted:         sess.Muted,
		VideoEnabled:  sess.VideoEnabled,
		ScreenSharing: sess.ScreenSharing,
	}

	payloadData, _ := json.Marshal(payload)
	packet := make([]byte, 1+len(payloadData))
	packet[0] = protocol.PacketTypeMediaState
	copy(packet[1:], payloadData)

	for _, other := range sessions {
		if other.ID == sess.ID {
			continue
		}
		to := other.GetAddr()
		if to == nil {
			continue
		}
		_ = s.send(packet, to)
	}
}

func (s *Server) send(data []byte, addr *net.UDPAddr) error {
	_, err := s.conn.WriteToUDP(data, addr)
	if err == nil && s.metrics != nil {
		s.metrics.RecordPacketSent(uint64(len(data)))
	}
	return err
}
