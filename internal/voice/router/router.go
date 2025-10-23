package router

import (
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

	for i := 0; i < 4; i++ {
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
	roomID := intToString(packet.Header.RoomID)

	sessions := r.sessionManager.GetRoomSessions(roomID)

	for _, session := range sessions {
		if session.Addr.String() == fromAddr.String() {
			continue
		}

		if session.Muted && packet.Header.Type == protocol.PacketTypeMedia {
			continue
		}

		select {
		case r.sendQueue <- &sendTask{
			packet: packet,
			addr:   session.GetAddr(),
		}:
		default:
			r.logger.Warn("send queue full, dropping packet")
		}
	}
}

func (r *Router) sendWorker() {
	defer r.wg.Done()

	for {
		select {
		case task := <-r.sendQueue:
			r.logger.Debug("routing packet would happen here")
			_ = task
		case <-r.stopChan:
			return
		}
	}
}

func intToString(val uint32) string {
	return string(rune(val))
}
