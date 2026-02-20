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

type ServerPool struct {
	conns      []*net.UDPConn
	ports      []int
	portToConn map[int]*net.UDPConn

	sessionManager *session.Manager
	router         *router.Router
	validator      *voiceauth.Validator
	logger         *zap.Logger
	metrics        *telemetry.Metrics

	workChan   chan *poolJob
	packetPool sync.Pool
	stopChan   chan struct{}
	wg         sync.WaitGroup
}

type poolJob struct {
	data []byte
	addr *net.UDPAddr
	conn *net.UDPConn
}

func NewServerPool(
	host string,
	startPort, count int,
	sessionManager *session.Manager,
	voiceRouter *router.Router,
	jwtManager *jwt.Manager,
	logger *zap.Logger,
	metrics *telemetry.Metrics,
) (*ServerPool, error) {
	pool := &ServerPool{
		conns:          make([]*net.UDPConn, 0, count),
		ports:          make([]int, 0, count),
		portToConn:     make(map[int]*net.UDPConn),
		sessionManager: sessionManager,
		router:         voiceRouter,
		validator:      voiceauth.NewValidator(jwtManager),
		logger:         logger,
		metrics:        metrics,
		workChan:       make(chan *poolJob, workChanSize),
		stopChan:       make(chan struct{}),
		packetPool: sync.Pool{
			New: func() interface{} {
				b := make([]byte, maxPacketLen)
				return &b
			},
		},
	}

	for i := 0; i < count; i++ {
		port := startPort + i
		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, port))
		if err != nil {
			pool.closeAll()
			return nil, fmt.Errorf("resolve port %d: %w", port, err)
		}

		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			pool.closeAll()
			return nil, fmt.Errorf("listen port %d: %w", port, err)
		}

		_ = conn.SetReadBuffer(4 * 1024 * 1024)
		_ = conn.SetWriteBuffer(4 * 1024 * 1024)

		pool.conns = append(pool.conns, conn)
		pool.ports = append(pool.ports, port)
		pool.portToConn[port] = conn
	}

	logger.Info("UDP pool created",
		zap.Int("port_start", startPort),
		zap.Int("count", count),
	)

	return pool, nil
}

func (p *ServerPool) Ports() []int {
	return p.ports
}

func (p *ServerPool) ConnForPort(port int) *net.UDPConn {
	return p.portToConn[port]
}

func (p *ServerPool) Start(ctx context.Context) error {
	for i := 0; i < numWorkers; i++ {
		p.wg.Add(1)
		go p.worker()
	}

	for _, conn := range p.conns {
		p.wg.Add(1)
		go p.readLoop(conn)
	}

	<-ctx.Done()
	close(p.stopChan)

	p.closeAll()
	p.wg.Wait()
	p.logger.Info("UDP pool stopped")
	return nil
}

func (p *ServerPool) closeAll() {
	for _, conn := range p.conns {
		_ = conn.Close()
	}
}

func (p *ServerPool) readLoop(conn *net.UDPConn) {
	defer p.wg.Done()

	for {
		bufPtr := p.packetPool.Get().(*[]byte)
		buf := *bufPtr

		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			p.packetPool.Put(bufPtr)
			select {
			case <-p.stopChan:
				return
			default:
				continue
			}
		}

		if n > maxPacketLen {
			p.packetPool.Put(bufPtr)
			continue
		}

		data := make([]byte, n)
		copy(data, buf[:n])
		p.packetPool.Put(bufPtr)

		select {
		case p.workChan <- &poolJob{data: data, addr: addr, conn: conn}:
		default:
			if p.metrics != nil {
				p.metrics.RecordPacketDropped()
			}
		}
	}
}

func (p *ServerPool) worker() {
	defer p.wg.Done()

	for {
		select {
		case job := <-p.workChan:
			if job != nil {
				p.handlePacket(job.data, job.addr, job.conn)
			}
		case <-p.stopChan:
			return
		}
	}
}

func (p *ServerPool) handlePacket(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < 1 {
		return
	}

	if p.metrics != nil {
		p.metrics.RecordPacketReceived(uint64(len(data)))
	}

	switch data[0] {
	case protocol.PacketTypeHello:
		p.handleHello(data, addr, conn)
	case protocol.PacketTypeAudio, protocol.PacketTypeVideo:
		p.handleMedia(data, addr, conn)
	case protocol.PacketTypePing:
		p.handlePing(data, addr, conn)
	case protocol.PacketTypeBye:
		p.handleBye(data, addr, conn)
	case protocol.PacketTypeSpeaking:
		p.handleSpeaking(data, addr, conn)
	case protocol.PacketTypeMediaState:
		p.handleMediaState(data, addr, conn)
	case protocol.PacketTypeNack:
		p.handleNack(data, addr, conn)
	case protocol.PacketTypePli:
		p.handlePli(data, addr, conn)
	case protocol.PacketTypeRR:
		p.handleReceiverReport(data, addr, conn)
	case protocol.PacketTypeSubscribe:
		p.handleSubscribe(data, addr, conn)
	}
}

func (p *ServerPool) handleMedia(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < protocol.MediaHeaderSize {
		return
	}

	pkt, err := protocol.ParsePacket(data)
	if err != nil {
		return
	}

	sess := p.sessionManager.GetBySSRC(pkt.Header.SSRC)
	if sess == nil {
		return
	}

	p.sessionManager.BindAddr(sess.ID, addr)
	p.sessionManager.Touch(sess.ID)
	sess.StoreForRetransmit(pkt.Header.Sequence, data)
	p.router.RouteMediaRaw(pkt.Header, data, addr, conn)
}

func (p *ServerPool) handleHello(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var hello protocol.HelloPayload
	if err := json.Unmarshal(data[1:], &hello); err != nil {
		return
	}

	claims, err := p.validator.ValidateToken(context.Background(), hello.Token)
	if err != nil {
		p.logger.Warn("invalid token in hello", zap.Error(err))
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

	existing := p.sessionManager.GetSessionByUserInRoom(claims.UserID, claims.RoomID)
	if existing != nil {
		p.broadcastParticipantLeft(existing.RoomID, existing.UserID, existing.SSRC, existing.VideoSSRC, conn)
		p.sessionManager.RemoveSession(existing.ID)
	}

	existingSessions := p.sessionManager.GetRoomSessions(claims.RoomID)
	sess := p.sessionManager.CreateSession(claims.UserID, claims.RoomID, addr, sessionCrypto, hello.VideoEnabled)

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
	_ = p.send(out, addr, conn)

	p.broadcastJoined(claims.RoomID, sess, conn)

	p.logger.Info("session created via pool",
		zap.String("user_id", claims.UserID),
		zap.String("room_id", claims.RoomID),
		zap.Uint32("ssrc", sess.SSRC),
	)
}

func (p *ServerPool) handlePing(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if sess := p.sessionManager.GetByAddr(addr); sess != nil {
		p.sessionManager.Touch(sess.ID)
	}

	pong := make([]byte, len(data))
	pong[0] = protocol.PacketTypePong
	copy(pong[1:], data[1:])
	_ = p.send(pong, addr, conn)
}

func (p *ServerPool) handleBye(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < 5 {
		return
	}

	ssrc := binary.BigEndian.Uint32(data[1:5])
	sess := p.sessionManager.GetBySSRC(ssrc)
	if sess == nil {
		return
	}

	roomID := sess.RoomID
	userID := sess.UserID
	videoSSRC := sess.VideoSSRC

	p.sessionManager.RemoveSession(sess.ID)
	p.broadcastParticipantLeft(roomID, userID, ssrc, videoSSRC, conn)
}

func (p *ServerPool) handleSpeaking(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var speaking protocol.SpeakingPayload
	if err := json.Unmarshal(data[1:], &speaking); err != nil {
		return
	}

	sess := p.sessionManager.GetBySSRC(speaking.SSRC)
	if sess == nil {
		return
	}

	sess.SetSpeaking(speaking.Speaking)
	p.sessionManager.Touch(sess.ID)

	sessions := p.sessionManager.GetRoomSessions(sess.RoomID)
	payload, _ := json.Marshal(speaking)
	pkt := make([]byte, 1+len(payload))
	pkt[0] = protocol.PacketTypeSpeaking
	copy(pkt[1:], payload)

	for _, s := range sessions {
		if s.SSRC == speaking.SSRC {
			continue
		}
		if to := s.GetAddr(); to != nil {
			_ = p.send(pkt, to, conn)
		}
	}
}

func (p *ServerPool) handleMediaState(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var ms protocol.MediaStatePayload
	if err := json.Unmarshal(data[1:], &ms); err != nil {
		return
	}

	sess := p.sessionManager.GetBySSRC(ms.SSRC)
	if sess == nil {
		return
	}

	sess.SetMuted(ms.Muted)
	sess.SetVideoEnabled(ms.VideoEnabled)
	sess.SetScreenSharing(ms.ScreenSharing)
	p.sessionManager.Touch(sess.ID)
	p.broadcastMediaState(sess.RoomID, sess, conn)
}

func (p *ServerPool) handleNack(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	nack, err := protocol.ParseNack(data)
	if err != nil {
		return
	}

	target := p.sessionManager.GetBySSRC(nack.SSRC)
	if target == nil {
		return
	}

	for _, seq := range nack.Sequences {
		if cached := target.GetForRetransmit(seq); cached != nil {
			_ = p.send(cached, addr, conn)
		}
	}
}

func (p *ServerPool) handlePli(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	pli, err := protocol.ParsePli(data)
	if err != nil {
		return
	}

	target := p.sessionManager.GetBySSRC(pli.SSRC)
	if target == nil {
		return
	}

	if to := target.GetAddr(); to != nil {
		_ = p.send(data, to, conn)
	}
}

func (p *ServerPool) handleReceiverReport(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	rr, err := protocol.ParseReceiverReport(data)
	if err != nil {
		return
	}

	target := p.sessionManager.GetBySSRC(rr.SSRC)
	if target == nil {
		return
	}

	if to := target.GetAddr(); to != nil {
		_ = p.send(data, to, conn)
	}
}

func (p *ServerPool) handleSubscribe(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	sess := p.sessionManager.GetByAddr(addr)
	if sess == nil {
		return
	}

	var payload protocol.SubscribePayload
	if err := json.Unmarshal(data[1:], &payload); err != nil {
		return
	}

	sess.UpdateSubscriptions(payload.Subscriptions)
}

func (p *ServerPool) broadcastJoined(roomID string, newSess *session.Session, conn *net.UDPConn) {
	sessions := p.sessionManager.GetRoomSessions(roomID)

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
			_ = p.send(pkt, to, conn)
		}
	}
}

func (p *ServerPool) broadcastParticipantLeft(roomID, userID string, ssrc, videoSSRC uint32, conn *net.UDPConn) {
	sessions := p.sessionManager.GetRoomSessions(roomID)

	payload := protocol.ParticipantLeftPayload{UserID: userID, RoomID: roomID, SSRC: ssrc, VideoSSRC: videoSSRC}
	data, _ := json.Marshal(payload)
	pkt := make([]byte, 1+len(data))
	pkt[0] = protocol.PacketTypeParticipantLeft
	copy(pkt[1:], data)

	for _, s := range sessions {
		if to := s.GetAddr(); to != nil {
			_ = p.send(pkt, to, conn)
		}
	}
}

func (p *ServerPool) broadcastMediaState(roomID string, sess *session.Session, conn *net.UDPConn) {
	sessions := p.sessionManager.GetRoomSessions(roomID)

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
			_ = p.send(pkt, to, conn)
		}
	}
}

func (p *ServerPool) send(data []byte, addr *net.UDPAddr, conn *net.UDPConn) error {
	_, err := conn.WriteToUDP(data, addr)
	if err == nil && p.metrics != nil {
		p.metrics.RecordPacketSent(uint64(len(data)))
	}
	return err
}
