package router

import (
	"net"
	"runtime"
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

// PacketOwner is implemented by the UDP packet buffer.
// Router does not need to know the concrete type.
type PacketOwner interface {
	Retain()
	Release()
}

type Router struct {
	sessionManager *session.Manager
	logger         *zap.Logger
	metrics        *telemetry.Metrics
	sendQueues     []chan *sendTask
	wg             sync.WaitGroup
	stopChan       chan struct{}
	numWorkers     int

	sendTaskPool sync.Pool
}

type sendTask struct {
	data  []byte
	owner PacketOwner
	addr  *net.UDPAddr
	conn  *net.UDPConn
}

func NewRouter(sessionManager *session.Manager, logger *zap.Logger, metrics *telemetry.Metrics) *Router {
	workers := runtime.NumCPU() * 2
	if workers < 4 {
		workers = 4
	}
	if workers > 32 {
		workers = 32
	}

	r := &Router{
		sessionManager: sessionManager,
		logger:         logger,
		metrics:        metrics,
		sendQueues:     make([]chan *sendTask, workers),
		stopChan:       make(chan struct{}),
		numWorkers:     workers,
		sendTaskPool: sync.Pool{
			New: func() interface{} {
				return new(sendTask)
			},
		},
	}

	for i := 0; i < workers; i++ {
		r.sendQueues[i] = make(chan *sendTask, 8192)
		r.wg.Add(1)
		go r.sendWorker(r.sendQueues[i])
	}

	return r
}

func (r *Router) Stop() {
	close(r.stopChan)
	for _, q := range r.sendQueues {
		close(q)
	}
	r.wg.Wait()
}

func (r *Router) getSendTask(data []byte, owner PacketOwner, addr *net.UDPAddr, conn *net.UDPConn) *sendTask {
	t := r.sendTaskPool.Get().(*sendTask)
	t.data = data
	t.owner = owner
	t.addr = addr
	t.conn = conn
	return t
}

func (r *Router) putSendTask(t *sendTask) {
	if t == nil {
		return
	}
	t.data = nil
	t.owner = nil
	t.addr = nil
	t.conn = nil
	r.sendTaskPool.Put(t)
}

func (r *Router) enqueue(queueIdx int, data []byte, owner PacketOwner, addr *net.UDPAddr, conn *net.UDPConn) bool {
	if owner != nil {
		owner.Retain()
	}

	task := r.getSendTask(data, owner, addr, conn)

	select {
	case r.sendQueues[queueIdx] <- task:
		return true
	default:
		if owner != nil {
			owner.Release()
		}
		r.putSendTask(task)
		if r.metrics != nil {
			r.metrics.RecordPacketDropped()
		}
		return false
	}
}

func (r *Router) RouteMediaRaw(h protocol.MediaHeader, raw []byte, fromAddr *net.UDPAddr, conn *net.UDPConn) {
	r.routeMedia(h, raw, nil, fromAddr, conn)
}

func (r *Router) RouteMediaOwned(h protocol.MediaHeader, raw []byte, owner PacketOwner, fromAddr *net.UDPAddr, conn *net.UDPConn) {
	r.routeMedia(h, raw, owner, fromAddr, conn)
}

func (r *Router) routeMedia(h protocol.MediaHeader, raw []byte, owner PacketOwner, fromAddr *net.UDPAddr, conn *net.UDPConn) {
	sender := r.sessionManager.GetBySSRC(h.SSRC)
	if sender == nil {
		return
	}
	roomID := sender.RoomID
	if roomID == "" {
		return
	}

	if h.Type == protocol.PacketTypeAudio && sender.Muted {
		return
	}

	sessions := r.sessionManager.GetRoomSessions(roomID)
	senderID := sender.ID
	routed := 0

	for _, dst := range sessions {
		if dst == nil || dst.ID == senderID {
			continue
		}

		to := dst.GetAddr()
		if to == nil {
			continue
		}

		if !dst.IsSubscribedTo(h.SSRC) {
			continue
		}

		qi := int(dst.ID) % r.numWorkers
		if r.enqueue(qi, raw, owner, to, conn) {
			routed++
		}
	}

	if r.metrics != nil && routed > 0 {
		if h.Type == protocol.PacketTypeAudio {
			r.metrics.RecordAudioOutN(uint64(routed))
		} else {
			r.metrics.RecordVideoOutN(uint64(routed))
		}
		r.metrics.RecordRoomRouted(roomID, uint64(len(raw)*routed))
	}
}

func (r *Router) RouteControlRaw(raw []byte, fromAddr *net.UDPAddr, conn *net.UDPConn, roomID string, excludeSSRC uint32) {
	for _, dst := range r.sessionManager.GetRoomSessions(roomID) {
		if dst == nil || dst.SSRC == excludeSSRC {
			continue
		}

		to := dst.GetAddr()
		if to == nil {
			continue
		}

		qi := int(dst.ID) % r.numWorkers
		_ = r.enqueue(qi, raw, nil, to, conn)
	}
}

func (r *Router) sendWorker(queue chan *sendTask) {
	defer r.wg.Done()

	for task := range queue {
		if task == nil {
			continue
		}

		if task.conn != nil && task.addr != nil && len(task.data) > 0 {
			if _, err := task.conn.WriteToUDP(task.data, task.addr); err != nil {
				r.logger.Debug("send fail", zap.String("to", task.addr.String()), zap.Error(err))
			} else if r.metrics != nil {
				r.metrics.RecordPacketSent(uint64(len(task.data)))
			}
		}

		if task.owner != nil {
			task.owner.Release()
		}
		r.putSendTask(task)
	}
}
