package router

import (
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/voice/congestion"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

// PacketOwner is a reference-counted packet buffer the router shares across
// destinations. The router Retains once per enqueue and Releases after each send
// (or drop), so the buffer returns to its pool only when the last recipient is
// done. A missed Release leaks; a double Release corrupts pool reuse.
type PacketOwner interface {
	Retain()
	Release()
}

// queueKind selects a per-worker priority lane: control drains before audio,
// audio before video, so voice stays crisp when the pipe is congested.
type queueKind uint8

const (
	qControl queueKind = iota
	qAudio
	qVideo
)

// Router is the SFU forwarding engine. It owns numWorkers worker goroutines, each
// draining a control/audio/video queue triple in strict priority; a destination
// SSRC is hashed to a fixed worker so its egress stays single-threaded. sendTasks
// are pooled to avoid per-packet allocation.
type Router struct {
	sessionManager *session.Manager
	logger         *zap.Logger
	metrics        *telemetry.Metrics
	ctrl           *congestion.Controller
	defaultConn    atomic.Pointer[net.UDPConn]

	controlQueues []chan *sendTask
	audioQueues   []chan *sendTask
	videoQueues   []chan *sendTask

	wg         sync.WaitGroup
	stopChan   chan struct{}
	numWorkers int

	sendTaskPool sync.Pool
}

// sendTask is one queued egress unit. owner (if non-nil) holds a buffer reference
// that must be Released when the task completes. transport routes over TCP; addr +
// conn route over UDP. created is the enqueue time in unix micros, used to age out
// stale media. control marks the task as never-aged control traffic.
type sendTask struct {
	data      []byte
	owner     PacketOwner
	addr      *net.UDPAddr
	conn      *net.UDPConn
	transport session.Transport
	control   bool
	created   int64
}

// NewRouter builds and starts the router: it sizes the worker pool to NumCPU*2
// clamped to [4,32], allocates per-worker priority queues (small control, larger
// audio/video), and launches one sendWorker per index. ctrl may be nil to disable
// congestion control (the legacy single-socket path passes nil).
func NewRouter(sessionManager *session.Manager, logger *zap.Logger, metrics *telemetry.Metrics, ctrl *congestion.Controller) *Router {
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
		ctrl:           ctrl,
		controlQueues:  make([]chan *sendTask, workers),
		audioQueues:    make([]chan *sendTask, workers),
		videoQueues:    make([]chan *sendTask, workers),
		stopChan:       make(chan struct{}),
		numWorkers:     workers,
		sendTaskPool: sync.Pool{New: func() interface{} {
			return new(sendTask)
		}},
	}

	for i := 0; i < workers; i++ {
		r.controlQueues[i] = make(chan *sendTask, 256)
		r.audioQueues[i] = make(chan *sendTask, 1024)
		r.videoQueues[i] = make(chan *sendTask, 1024)
		r.wg.Add(1)
		go r.sendWorker(r.controlQueues[i], r.audioQueues[i], r.videoQueues[i])
	}

	return r
}

// Stop signals shutdown, closes every worker queue, and blocks until all workers
// have drained and exited. Not safe to call more than once.
func (r *Router) Stop() {
	close(r.stopChan)
	for i := 0; i < len(r.controlQueues); i++ {
		close(r.controlQueues[i])
		close(r.audioQueues[i])
		close(r.videoQueues[i])
	}
	r.wg.Wait()
}

// SetDefaultConn sets the UDP socket used to reach UDP peers when a packet has
// no origin socket of its own — media that arrived over the TCP fallback
// transport. Call once at startup with the pool's primary socket.
func (r *Router) SetDefaultConn(conn *net.UDPConn) {
	r.defaultConn.Store(conn)
}

// getSendTask fetches a pooled sendTask and populates it, stamping created with
// the current time so media can be aged out. Pair with putSendTask.
func (r *Router) getSendTask(data []byte, owner PacketOwner, addr *net.UDPAddr, conn *net.UDPConn, transport session.Transport, control bool) *sendTask {
	t := r.sendTaskPool.Get().(*sendTask)
	t.data = data
	t.owner = owner
	t.addr = addr
	t.conn = conn
	t.transport = transport
	t.control = control
	t.created = time.Now().UnixMicro()
	return t
}

// putSendTask clears a task's fields (dropping references so they can be GC'd)
// and returns it to the pool. It does not Release owner — the caller must do that
// first. Nil-safe.
func (r *Router) putSendTask(t *sendTask) {
	if t == nil {
		return
	}
	t.data = nil
	t.owner = nil
	t.addr = nil
	t.conn = nil
	t.transport = nil
	t.control = false
	t.created = 0
	r.sendTaskPool.Put(t)
}

// workerIndexFor maps a destination SSRC to a worker index via a Knuth
// multiplicative hash, so all packets for one destination land on the same worker
// and its egress is serialized without a lock.
func workerIndexFor(ssrc uint32, n int) int {
	if n <= 1 {
		return 0
	}
	v := ssrc * 2654435761
	return int(v % uint32(n))
}

// enqueue Retains owner and pushes a task onto worker queueIdx's lane for kind.
// It is non-blocking: if the queue is full it Releases owner, returns the task to
// the pool, records a per-kind drop metric, and returns false. Returns true when
// the task was accepted (and the worker will Release owner after sending).
func (r *Router) enqueue(queueIdx int, data []byte, owner PacketOwner, addr *net.UDPAddr, conn *net.UDPConn, transport session.Transport, kind queueKind) bool {
	if owner != nil {
		owner.Retain()
	}
	control := kind == qControl
	task := r.getSendTask(data, owner, addr, conn, transport, control)

	var queue chan *sendTask
	switch kind {
	case qControl:
		queue = r.controlQueues[queueIdx]
	case qAudio:
		queue = r.audioQueues[queueIdx]
	default:
		queue = r.videoQueues[queueIdx]
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
			switch kind {
			case qControl:
				r.metrics.RecordControlDropped()
			case qAudio:
				r.metrics.RecordAudioDropped()
			default:
				r.metrics.RecordVideoDropped()
			}
		}
		return false
	}
}

// RouteMediaRaw forwards media whose buffer is not reference-counted (e.g.
// TCP-origin frames); the router copies nothing and holds no owner, so raw must
// stay valid only until this returns.
func (r *Router) RouteMediaRaw(h protocol.MediaHeader, raw []byte, fromAddr *net.UDPAddr, conn *net.UDPConn) {
	r.routeMedia(h, raw, nil, fromAddr, conn)
}

// RouteMediaOwned forwards media backed by a pooled buffer. owner is Retained once
// per accepted destination and Released after each send, keeping raw alive across
// all fan-out sends without a copy.
func (r *Router) RouteMediaOwned(h protocol.MediaHeader, raw []byte, owner PacketOwner, fromAddr *net.UDPAddr, conn *net.UDPConn) {
	r.routeMedia(h, raw, owner, fromAddr, conn)
}

// routeMedia fans one media packet out to every subscribed, non-observer peer in
// the sender's room. It drops audio from a muted sender, applies opt-in simulcast
// layer selection (only when the receiver set a QualityPref, capped by congestion
// state), hashes each destination to a worker, and records out/no-subscriber
// metrics. Nothing is forwarded if the sender is unknown or roomless.
func (r *Router) routeMedia(h protocol.MediaHeader, raw []byte, owner PacketOwner, fromAddr *net.UDPAddr, conn *net.UDPConn) {
	sender := r.sessionManager.GetBySSRC(h.SSRC)
	if sender == nil || sender.RoomID == "" {
		return
	}
	if h.Type == protocol.PacketTypeAudio && sender.Muted {
		return
	}

	kind := qAudio
	var now time.Time
	if h.Type == protocol.PacketTypeVideo {
		kind = qVideo
		if r.ctrl != nil {
			now = time.Now()
			r.ctrl.ObserveLayer(h.SSRC, h.Layer, now)
		}
	}

	sessions := r.sessionManager.GetRoomSessions(sender.RoomID)
	senderID := sender.ID
	routed := 0
	for _, dst := range sessions {
		if dst == nil || dst.ID == senderID || dst.IsObserver {
			continue
		}
		tp := dst.Transport()
		to := dst.GetAddr()
		if tp == nil && to == nil {
			continue
		}
		if !dst.IsSubscribedTo(h.SSRC) {
			continue
		}

		// Simulcast layer selection is OPT-IN: it applies only when this receiver
		// set a QualityPref for the stream. A sender may tag a single stream with
		// a varying Layer byte, so without an explicit preference we forward every
		// layer — dropping "non-target" packets there would shred the one stream
		// and the receiver would decode nothing. With a preference, forward the
		// highest produced layer at or below it (capped by RR-loss congestion).
		// See congestion.TargetLayer for the shared-sequence caveat.
		if h.Type == protocol.PacketTypeVideo {
			if pref, ok := dst.GetQualityPref(h.SSRC); ok {
				if r.ctrl != nil {
					effTier := r.ctrl.EffectiveTier(h.SSRC, dst.ID, pref)
					if target, ok := r.ctrl.TargetLayer(h.SSRC, effTier, now); ok && h.Layer != target {
						continue
					}
				} else if h.Layer != pref {
					continue
				}
			}
		}

		qi := workerIndexFor(dst.SSRC, r.numWorkers)
		if r.enqueue(qi, raw, owner, to, conn, tp, kind) {
			routed++
		}
	}

	if r.metrics != nil {
		if routed > 0 {
			if h.Type == protocol.PacketTypeAudio {
				r.metrics.RecordAudioOutN(uint64(routed))
			} else {
				r.metrics.RecordVideoOutN(uint64(routed))
			}
			r.metrics.RecordRoomRouted(sender.RoomID, uint64(len(raw)*routed))
		} else if h.Type == protocol.PacketTypeVideo {
			// Received video that reached no receiver — diagnostic signal for
			// "video in but nothing out" (no subscriber / all layers dropped).
			r.metrics.RecordVideoNoSubscriber()
		}
	}
}

// RouteControlRoom enqueues a control packet to every session in roomID except
// excludeSessionID (pass 0 to include all). Control tasks carry no owner and are
// never aged out. Drops on a full queue are ignored.
func (r *Router) RouteControlRoom(raw []byte, conn *net.UDPConn, roomID string, excludeSessionID uint32) {
	for _, dst := range r.sessionManager.GetRoomSessions(roomID) {
		if dst == nil || dst.ID == excludeSessionID {
			continue
		}
		tp := dst.Transport()
		to := dst.GetAddr()
		if tp == nil && to == nil {
			continue
		}
		qi := workerIndexFor(dst.SSRC, r.numWorkers)
		_ = r.enqueue(qi, raw, nil, to, conn, tp, qControl)
	}
}

// RouteControlToSession enqueues a control packet to a single destination,
// returning false if dst is nil, has no transport/address, or its queue is full.
func (r *Router) RouteControlToSession(raw []byte, conn *net.UDPConn, dst *session.Session) bool {
	if dst == nil {
		return false
	}
	tp := dst.Transport()
	to := dst.GetAddr()
	if tp == nil && to == nil {
		return false
	}
	qi := workerIndexFor(dst.SSRC, r.numWorkers)
	return r.enqueue(qi, raw, nil, to, conn, tp, qControl)
}

// maxMediaAgeUs is the age (in microseconds) past which a queued media task is
// dropped at send time to shed backlog rather than deliver stale audio/video.
const maxMediaAgeUs = 80_000

// sendWorker drains its three queues in strict control>audio>video priority using
// nested non-blocking selects, exiting on stopChan. For each task it ages out
// stale media (see maxMediaAgeUs), writes over TCP transport or UDP (falling back
// to the default conn for TCP-origin packets), Releases the owner, and returns the
// task to the pool.
func (r *Router) sendWorker(controlQ, audioQ, videoQ chan *sendTask) {
	defer r.wg.Done()
	for {
		var task *sendTask
		// Strict priority: control first, then audio, then video.
		select {
		case <-r.stopChan:
			return
		case task = <-controlQ:
		default:
			select {
			case <-r.stopChan:
				return
			case task = <-controlQ:
			case task = <-audioQ:
			default:
				select {
				case <-r.stopChan:
					return
				case task = <-controlQ:
				case task = <-audioQ:
				case task = <-videoQ:
				}
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

		if !task.control && task.created > 0 {
			age := time.Now().UnixMicro() - task.created
			if age > maxMediaAgeUs {
				if task.owner != nil {
					task.owner.Release()
				}
				if r.metrics != nil {
					r.metrics.RecordPacketDropped()
				}
				r.putSendTask(task)
				continue
			}
		}

		if task.transport != nil && len(task.data) > 0 {
			if err := task.transport.WritePacket(task.data); err != nil {
				r.logger.Debug("tcp send fail", zap.Error(err))
			} else if r.metrics != nil {
				r.metrics.RecordPacketSent(uint64(len(task.data)))
				if task.control {
					r.metrics.RecordControlSent()
				}
			}
		} else if task.addr != nil && len(task.data) > 0 {
			// UDP destination. Use the packet's origin socket, or fall back to
			// the default socket for transport-less (TCP-origin) packets so a
			// TCP-fallback sender can still reach UDP peers.
			conn := task.conn
			if conn == nil {
				conn = r.defaultConn.Load()
			}
			if conn != nil {
				if _, err := conn.WriteToUDP(task.data, task.addr); err != nil {
					r.logger.Debug("send fail", zap.String("to", task.addr.String()), zap.Error(err))
				} else if r.metrics != nil {
					r.metrics.RecordPacketSent(uint64(len(task.data)))
					if task.control {
						r.metrics.RecordControlSent()
					}
				}
			}
		}
		if task.owner != nil {
			task.owner.Release()
		}
		r.putSendTask(task)
	}
}
