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
	data []byte
	addr *net.UDPAddr
	conn *net.UDPConn
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

func (r *Router) RoutePacket(packet *protocol.Packet, fromAddr *net.UDPAddr, conn *net.UDPConn) {
	pktType := packet.Header.Type

	switch pktType {
	case protocol.PacketTypeAudio, protocol.PacketTypeVideo:
		sess := r.sessionManager.GetByAddr(fromAddr)
		if sess == nil {
			r.logger.Debug("unknown sender session", zap.String("from", fromAddr.String()))
			return
		}

		roomID := sess.RoomID
		if roomID == "" {
			r.logger.Debug("sender session missing room id", zap.Uint32("ssrc", sess.SSRC))
			return
		}

		sessions := r.sessionManager.GetRoomSessions(roomID)
		if len(sessions) == 0 {
			r.logger.Debug("no sessions in room", zap.String("room_id", roomID))
			return
		}

		data := packet.Marshal()
		r.routeMediaPacket(data, packet, fromAddr, conn, sessions)
		return

	case protocol.PacketTypeSpeaking:
		roomID := packet.GetRoomIDString()
		if roomID == "" {
			r.logger.Debug("speaking packet missing room id")
			return
		}

		sessions := r.sessionManager.GetRoomSessions(roomID)
		if len(sessions) == 0 {
			r.logger.Debug("no sessions in room", zap.String("room_id", roomID))
			return
		}

		data := packet.Marshal()
		r.routeSpeakingPacket(data, fromAddr, conn, sessions)
		return
	}
}

func sameUDPAddr(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Port != b.Port {
		return false
	}
	if a.IP == nil || b.IP == nil {
		return a.String() == b.String()
	}
	return a.IP.Equal(b.IP)
}

func (r *Router) routeMediaPacket(
	data []byte,
	packet *protocol.Packet,
	fromAddr *net.UDPAddr,
	conn *net.UDPConn,
	sessions []*session.Session,
) {
	for _, sess := range sessions {
		to := sess.GetAddr()
		if to == nil {
			continue
		}

		if sameUDPAddr(to, fromAddr) {
			continue
		}

		select {
		case r.sendQueue <- &sendTask{
			data: data,
			addr: to,
			conn: conn,
		}:
		default:
			r.logger.Warn("send queue full, dropping packet",
				zap.Uint32("ssrc", sess.SSRC),
			)
		}
	}
}

func (r *Router) routeSpeakingPacket(
	data []byte,
	fromAddr *net.UDPAddr,
	conn *net.UDPConn,
	sessions []*session.Session,
) {
	for _, sess := range sessions {
		to := sess.GetAddr()
		if to == nil {
			continue
		}

		if sameUDPAddr(to, fromAddr) {
			continue
		}

		select {
		case r.sendQueue <- &sendTask{
			data: data,
			addr: to,
			conn: conn,
		}:
		default:
			r.logger.Warn("send queue full, dropping speaking packet",
				zap.Uint32("ssrc", sess.SSRC),
			)
		}
	}
}

func (r *Router) sendWorker() {
	defer r.wg.Done()

	for {
		select {
		case task, ok := <-r.sendQueue:
			if !ok {
				return
			}
			if task == nil || task.conn == nil || task.addr == nil {
				continue
			}

			if _, err := task.conn.WriteToUDP(task.data, task.addr); err != nil {
				r.logger.Debug("failed to send packet",
					zap.String("to", task.addr.String()),
					zap.Error(err),
				)
			}

		case <-r.stopChan:
			return
		}
	}
}

func (r *Router) RouteMediaRaw(h protocol.MediaHeader, raw []byte, fromAddr *net.UDPAddr, conn *net.UDPConn) {
	sender := r.sessionManager.GetBySSRC(h.SSRC)
	if sender == nil {
		r.logger.Debug("unknown sender SSRC", zap.Uint32("ssrc", h.SSRC))
		return
	}

	roomID := sender.RoomID
	if roomID == "" {
		r.logger.Debug("sender missing room id", zap.Uint32("ssrc", sender.SSRC))
		return
	}

	sessions := r.sessionManager.GetRoomSessions(roomID)
	if len(sessions) == 0 {
		r.logger.Debug("no sessions in room", zap.String("room_id", roomID))
		return
	}

	pktType := h.Type

	for _, dst := range sessions {
		to := dst.GetAddr()
		if to == nil || sameUDPAddr(to, fromAddr) {
			continue
		}

		if pktType == protocol.PacketTypeVideo && !dst.VideoEnabled {
			continue
		}

		select {
		case r.sendQueue <- &sendTask{data: raw, addr: to, conn: conn}:
		default:
			r.logger.Warn("send queue full, dropping packet", zap.Uint32("ssrc", dst.SSRC))
		}
	}
}
