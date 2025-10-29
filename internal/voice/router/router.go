package router

import (
	"fmt"
	"net"
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"go.uber.org/zap"
)

type Router struct {
	sessionManager *session.Manager
	logger         *zap.Logger
	sendQueue      chan *sendTask
	wg             sync.WaitGroup
	stopChan       chan struct{}
}

type sendTask struct {
	packet *protocol.Packet
	addr   *net.UDPAddr
}

func NewRouter(sessionManager *session.Manager, logger *zap.Logger) *Router {
	r := &Router{
		sessionManager: sessionManager,
		logger:         logger,
		sendQueue:      make(chan *sendTask, 10000),
		stopChan:       make(chan struct{}),
	}

	numWorkers := 4
	for i := 0; i < numWorkers; i++ {
		r.wg.Add(1)
		go r.sendWorker()
	}

	return r
}

func (r *Router) Stop() {
	close(r.stopChan)
	r.wg.Wait()
}

func (r *Router) RoutePacket(packet *protocol.Packet, fromAddr *net.UDPAddr) {
	roomID := extractRoomID(packet.RoomID)
	if roomID == "" {
		r.logger.Debug("packet missing room ID")
		return
	}

	sessions := r.sessionManager.GetRoomSessions(roomID)
	if len(sessions) == 0 {
		return
	}

	switch packet.Type {
	case protocol.PacketTypeAudio, protocol.PacketTypeVideo:
		r.routeMediaPacket(packet, fromAddr, sessions)
	case protocol.PacketTypeSpeaking:
		r.routeSpeakingPacket(packet, fromAddr, sessions)
	}
}

func (r *Router) routeMediaPacket(packet *protocol.Packet, fromAddr *net.UDPAddr, sessions []*session.Session) {
	for _, sess := range sessions {
		if sess.Addr.String() == fromAddr.String() {
			continue
		}

		if sess.Muted && packet.Type == protocol.PacketTypeAudio {
			continue
		}

		select {
		case r.sendQueue <- &sendTask{
			packet: packet,
			addr:   sess.GetAddr(),
		}:
		default:
			r.logger.Warn("send queue full, dropping packet",
				zap.Uint32("ssrc", sess.SSRC),
			)
		}
	}
}

func (r *Router) routeSpeakingPacket(packet *protocol.Packet, fromAddr *net.UDPAddr, sessions []*session.Session) {
	for _, sess := range sessions {
		if sess.Addr.String() == fromAddr.String() {
			continue
		}

		select {
		case r.sendQueue <- &sendTask{
			packet: packet,
			addr:   sess.GetAddr(),
		}:
		default:
			r.logger.Warn("send queue full, dropping speaking packet")
		}
	}
}

func (r *Router) sendWorker() {
	defer r.wg.Done()

	for {
		select {
		case task := <-r.sendQueue:
			r.logger.Debug("routing packet",
				zap.Uint8("type", task.packet.Type),
				zap.Uint32("ssrc", task.packet.SSRC),
				zap.String("to", task.addr.String()),
			)

		case <-r.stopChan:
			return
		}
	}
}

func extractRoomID(roomIDBytes []byte) string {
	if len(roomIDBytes) == 0 {
		return ""
	}

	if len(roomIDBytes) == 16 {
		return uuidBytesToString(roomIDBytes)
	}

	return string(roomIDBytes)
}

func uuidBytesToString(b []byte) string {
	if len(b) != 16 {
		return ""
	}

	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3],
		b[4], b[5],
		b[6], b[7],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15],
	)
}
