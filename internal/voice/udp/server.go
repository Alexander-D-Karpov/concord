package udp

import (
	"context"
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
	"go.uber.org/zap"
)

type Server struct {
	conn           *net.UDPConn
	sessionManager *session.Manager
	router         *router.Router
	validator      *voiceauth.Validator
	logger         *zap.Logger
	stopChan       chan struct{}
	wg             sync.WaitGroup
}

func NewServer(
	host string,
	port int,
	sessionManager *session.Manager,
	router *router.Router,
	jwtManager *jwt.Manager,
	logger *zap.Logger,
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
		stopChan:       make(chan struct{}),
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	s.logger.Info("UDP server starting", zap.String("address", s.conn.LocalAddr().String()))

	numWorkers := 4
	for i := 0; i < numWorkers; i++ {
		s.wg.Add(1)
		go s.readLoop(i)
	}

	<-ctx.Done()
	close(s.stopChan)

	if err := s.conn.Close(); err != nil {
		return err
	}

	s.wg.Wait()
	s.logger.Info("UDP server stopped")
	return nil
}

func (s *Server) readLoop(workerID int) {
	defer s.wg.Done()
	buf := make([]byte, 65536)

	for {
		select {
		case <-s.stopChan:
			return
		default:
		}

		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.stopChan:
				return
			default:
				s.logger.Error("read UDP packet error", zap.Error(err))
				continue
			}
		}

		packetData := make([]byte, n)
		copy(packetData, buf[:n])
		go s.handlePacket(packetData, addr)
	}
}

func (s *Server) handlePacket(data []byte, addr *net.UDPAddr) {
	if len(data) < 1 {
		return
	}

	packetType := data[0]

	switch packetType {
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
	default:
		s.logger.Debug("unknown packet type", zap.Uint8("type", packetType))
	}
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

	key, err := crypto.GenerateKey()
	if err != nil {
		s.logger.Error("failed to generate key", zap.Error(err))
		return
	}

	cipher, err := crypto.NewCipher(key)
	if err != nil {
		s.logger.Error("failed to create cipher", zap.Error(err))
		return
	}

	sess := s.sessionManager.CreateSession(claims.UserID, claims.RoomID, addr, cipher)

	existingSessions := s.sessionManager.GetRoomSessions(claims.RoomID)
	participants := make([]protocol.ParticipantInfo, 0, len(existingSessions))
	for _, existing := range existingSessions {
		if existing.ID != sess.ID {
			participants = append(participants, protocol.ParticipantInfo{
				UserID:       existing.UserID,
				SSRC:         existing.SSRC,
				Muted:        existing.Muted,
				VideoEnabled: existing.VideoEnabled,
			})
		}
	}

	welcome := protocol.WelcomePayload{
		SessionID:    sess.ID,
		SSRC:         sess.SSRC,
		Participants: participants,
	}

	welcomeData, err := json.Marshal(welcome)
	if err != nil {
		s.logger.Error("failed to marshal welcome", zap.Error(err))
		return
	}

	packet := make([]byte, 1+len(welcomeData))
	packet[0] = protocol.PacketTypeWelcome
	copy(packet[1:], welcomeData)

	if err := s.send(packet, addr); err != nil {
		s.logger.Error("failed to send welcome", zap.Error(err))
		return
	}

	s.logger.Info("session created",
		zap.String("user_id", claims.UserID),
		zap.String("room_id", claims.RoomID),
		zap.Uint32("ssrc", sess.SSRC),
	)
}

func (s *Server) handleMedia(data []byte, addr *net.UDPAddr) {
	if len(data) < 20 {
		return
	}

	packet, err := protocol.ParsePacket(data)
	if err != nil {
		s.logger.Debug("failed to parse packet", zap.Error(err))
		return
	}

	s.router.RoutePacket(packet, addr)
}

func (s *Server) handlePing(data []byte, addr *net.UDPAddr) {
	pong := make([]byte, len(data))
	pong[0] = protocol.PacketTypePong
	copy(pong[1:], data[1:])

	if err := s.send(pong, addr); err != nil {
		s.logger.Error("failed to send pong", zap.Error(err))
	}
}

func (s *Server) handleBye(data []byte, addr *net.UDPAddr) {
	s.logger.Info("received bye", zap.String("addr", addr.String()))
}

func (s *Server) handleSpeaking(data []byte, addr *net.UDPAddr) {
	var speaking protocol.SpeakingPayload
	if err := json.Unmarshal(data[1:], &speaking); err != nil {
		s.logger.Error("failed to unmarshal speaking", zap.Error(err))
		return
	}

	sess := s.sessionManager.GetSession(speaking.SSRC)
	if sess != nil {
		sess.SetSpeaking(speaking.Speaking)

		// Broadcast to room
		s.broadcastSpeaking(sess.RoomID, speaking)
	}
}

func (s *Server) broadcastSpeaking(roomID string, speaking protocol.SpeakingPayload) {
	sessions := s.sessionManager.GetRoomSessions(roomID)

	payload, err := json.Marshal(speaking)
	if err != nil {
		return
	}

	packet := make([]byte, 1+len(payload))
	packet[0] = protocol.PacketTypeSpeaking
	copy(packet[1:], payload)

	for _, sess := range sessions {
		if err := s.send(packet, sess.GetAddr()); err != nil {
			s.logger.Debug("failed to send speaking notification", zap.Error(err))
		}
	}
}

func (s *Server) send(data []byte, addr *net.UDPAddr) error {
	_, err := s.conn.WriteToUDP(data, addr)
	return err
}
