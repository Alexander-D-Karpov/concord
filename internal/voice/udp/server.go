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
	err := s.conn.Close()
	if err != nil {
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
	packet, err := protocol.ParsePacket(data)
	if err != nil {
		s.logger.Debug("failed to parse packet", zap.Error(err))
		return
	}

	switch packet.Header.Type {
	case protocol.PacketTypeHello:
		s.handleHello(packet, addr)
	case protocol.PacketTypeMedia:
		s.handleMedia(packet, addr)
	case protocol.PacketTypePing:
		s.handlePing(packet, addr)
	case protocol.PacketTypeBye:
		s.handleBye(packet, addr)
	default:
		s.logger.Debug("unknown packet type", zap.Uint8("type", packet.Header.Type))
	}
}

func (s *Server) handleHello(packet *protocol.Packet, addr *net.UDPAddr) {
	var hello protocol.HelloPayload
	if err := json.Unmarshal(packet.Payload, &hello); err != nil {
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
				UserID:       uint32(existing.SSRC),
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

	welcomePacket := &protocol.Packet{
		Header: protocol.PacketHeader{
			Type:   protocol.PacketTypeWelcome,
			SSRC:   sess.SSRC,
			RoomID: packet.Header.RoomID,
		},
		Payload: welcomeData,
	}

	if err := s.SendPacket(welcomePacket, addr); err != nil {
		s.logger.Error("failed to send welcome", zap.Error(err))
		return
	}

	s.logger.Info("session created",
		zap.String("user_id", claims.UserID),
		zap.String("room_id", claims.RoomID),
		zap.Uint32("ssrc", sess.SSRC),
	)
}

func (s *Server) handleMedia(packet *protocol.Packet, addr *net.UDPAddr) {
	if len(packet.Payload) == 0 {
		return
	}

	s.router.RoutePacket(packet, addr)
}

func (s *Server) handlePing(packet *protocol.Packet, addr *net.UDPAddr) {
	pong := &protocol.Packet{
		Header: protocol.PacketHeader{
			Type:      protocol.PacketTypePong,
			Sequence:  packet.Header.Sequence,
			Timestamp: packet.Header.Timestamp,
			SSRC:      packet.Header.SSRC,
		},
	}

	if err := s.SendPacket(pong, addr); err != nil {
		s.logger.Error("failed to send pong", zap.Error(err))
	}
}

func (s *Server) handleBye(packet *protocol.Packet, addr *net.UDPAddr) {
	s.logger.Info("received bye", zap.String("addr", addr.String()))
}

func (s *Server) SendPacket(packet *protocol.Packet, addr *net.UDPAddr) error {
	data := packet.Marshal()
	_, err := s.conn.WriteToUDP(data, addr)
	return err
}
