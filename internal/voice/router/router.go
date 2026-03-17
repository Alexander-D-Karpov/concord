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

type PacketOwner interface {
	Retain()
	Release()
}

type Router struct {
	sessionManager *session.Manager
	logger         *zap.Logger
	metrics        *telemetry.Metrics

	controlQueues []chan *sendTask
	mediaQueues   []chan *sendTask

	wg         sync.WaitGroup
	stopChan   chan struct{}
	numWorkers int

	sendTaskPool sync.Pool
}

type sendTask struct {
	data    []byte
	owner   PacketOwner
	addr    *net.UDPAddr
	conn    *net.UDPConn
	control bool
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
		controlQueues:  make([]chan *sendTask, workers),
		mediaQueues:    make([]chan *sendTask, workers),
		stopChan:       make(chan struct{}),
		numWorkers:     workers,
		sendTaskPool: sync.Pool{New: func() interface{} {
			return new(sendTask)
		}},
	}

	for i := 0; i < workers; i++ {
		r.controlQueues[i] = make(chan *sendTask, 2048)
		r.mediaQueues[i] = make(chan *sendTask, 8192)
		r.wg.Add(1)
		go r.sendWorker(r.controlQueues[i], r.mediaQueues[i])
	}

	return r
}

func (r *Router) Stop() {
	close(r.stopChan)
	for i := 0; i < len(r.controlQueues); i++ {
		close(r.controlQueues[i])
		close(r.mediaQueues[i])
	}
	r.wg.Wait()
}

func (r *Router) getSendTask(data []byte, owner PacketOwner, addr *net.UDPAddr, conn *net.UDPConn, control bool) *sendTask {
	t := r.sendTaskPool.Get().(*sendTask)
	t.data = data
	t.owner = owner
	t.addr = addr
	t.conn = conn
	t.control = control
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
	t.control = false
	r.sendTaskPool.Put(t)
}

func workerIndexFor(ssrc uint32, n int) int {
	if n <= 1 {
		return 0
	}
	v := ssrc * 2654435761
	return int(v % uint32(n))
}

func (r *Router) enqueue(queueIdx int, data []byte, owner PacketOwner, addr *net.UDPAddr, conn *net.UDPConn, control bool) bool {
	if owner != nil {
		owner.Retain()
	}
	task := r.getSendTask(data, owner, addr, conn, control)
	queue := r.mediaQueues[queueIdx]
	if control {
		queue = r.controlQueues[queueIdx]
	}
	select {
	case queue <- task:
		return true
	default:
		if owner != nil {
			owner.Release()
		}
		r.putSendTask(task)
		if r.metrics != nil {
			if control {
				r.metrics.RecordControlDropped()
			} else {
				r.metrics.RecordPacketDropped()
			}
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
	if sender == nil || sender.RoomID == "" {
		return
	}
	if h.Type == protocol.PacketTypeAudio && sender.Muted {
		return
	}

	sessions := r.sessionManager.GetRoomSessions(sender.RoomID)
	senderID := sender.ID
	routed := 0
	for _, dst := range sessions {
		if dst == nil || dst.ID == senderID || dst.IsObserver {
			continue
		}
		to := dst.GetAddr()
		if to == nil {
			continue
		}
		if !dst.IsSubscribedTo(h.SSRC) {
			continue
		}

		if h.Type == protocol.PacketTypeVideo {
			desiredTier, hasPref := dst.GetQualityPref(h.SSRC)
			if hasPref && h.Layer != desiredTier {
				continue
			}
		}

		qi := workerIndexFor(dst.SSRC, r.numWorkers)
		if r.enqueue(qi, raw, owner, to, conn, false) {
			routed++
		}
	}

	if r.metrics != nil && routed > 0 {
		if h.Type == protocol.PacketTypeAudio {
			r.metrics.RecordAudioOutN(uint64(routed))
		} else {
			r.metrics.RecordVideoOutN(uint64(routed))
		}
		r.metrics.RecordRoomRouted(sender.RoomID, uint64(len(raw)*routed))
	}
}

func (r *Router) RouteControlRoom(raw []byte, conn *net.UDPConn, roomID string, excludeSessionID uint32) {
	for _, dst := range r.sessionManager.GetRoomSessions(roomID) {
		if dst == nil || dst.ID == excludeSessionID {
			continue
		}
		to := dst.GetAddr()
		if to == nil {
			continue
		}
		qi := workerIndexFor(dst.SSRC, r.numWorkers)
		_ = r.enqueue(qi, raw, nil, to, conn, true)
	}
}

func (r *Router) RouteControlToSession(raw []byte, conn *net.UDPConn, dst *session.Session) bool {
	if dst == nil {
		return false
	}
	to := dst.GetAddr()
	if to == nil {
		return false
	}
	qi := workerIndexFor(dst.SSRC, r.numWorkers)
	return r.enqueue(qi, raw, nil, to, conn, true)
}

func (r *Router) sendWorker(controlQ, mediaQ chan *sendTask) {
	defer r.wg.Done()
	for {
		var task *sendTask
		select {
		case task = <-controlQ:
		default:
			select {
			case task = <-controlQ:
			case task = <-mediaQ:
			case <-r.stopChan:
				return
			}
		}
		if task == nil {
			select {
			case <-r.stopChan:
				return
			default:
			}
			continue
		}

		if task.conn != nil && task.addr != nil && len(task.data) > 0 {
			if _, err := task.conn.WriteToUDP(task.data, task.addr); err != nil {
				r.logger.Debug("send fail", zap.String("to", task.addr.String()), zap.Error(err))
			} else if r.metrics != nil {
				r.metrics.RecordPacketSent(uint64(len(task.data)))
				if task.control {
					r.metrics.RecordControlSent()
				}
			}
		}
		if task.owner != nil {
			task.owner.Release()
		}
		r.putSendTask(task)
	}
}
