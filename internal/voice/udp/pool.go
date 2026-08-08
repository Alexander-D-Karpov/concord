package udp

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	voiceauth "github.com/Alexander-D-Karpov/concord/internal/voice/auth"
	"github.com/Alexander-D-Karpov/concord/internal/voice/congestion"
	"github.com/Alexander-D-Karpov/concord/internal/voice/router"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

// ServerPool is the production UDP data plane. It binds one or more sockets
// (single SO_REUSEPORT port, or a contiguous range) fed by read-loop goroutines,
// hands datagrams to a worker pool over workChan, and dispatches each through the
// shared Handler. portToConn/ports let a signaling layer advertise assignable
// ports.
type ServerPool struct {
	conns      []*net.UDPConn
	ports      []int
	portToConn map[int]*net.UDPConn

	handler *Handler
	logger  *zap.Logger
	metrics *telemetry.Metrics

	workChan   chan *poolJob
	packetPool *sync.Pool
	stopChan   chan struct{}
	wg         sync.WaitGroup
}

// poolJob carries one received datagram from a read loop to a worker: the pooled
// buffer, its source address, and the socket it arrived on (the reply socket).
type poolJob struct {
	pkt  *packetBuffer
	addr *net.UDPAddr
	conn *net.UDPConn
}

// NewServerPool constructs the pool and binds count sockets starting at startPort
// (all on startPort when singlePort is set, which requires SO_REUSEPORT). It wires
// a Handler with the congestion controller, sets 4 MiB socket buffers, and records
// the port-to-conn map. On any bind failure it closes all sockets and returns the
// error. It does not start goroutines; call Start.
func NewServerPool(
	host string,
	startPort, count int,
	singlePort bool,
	sessionManager *session.Manager,
	voiceRouter *router.Router,
	jwtManager *jwt.Manager,
	logger *zap.Logger,
	metrics *telemetry.Metrics,
	ctrl *congestion.Controller,
) (*ServerPool, error) {
	handler := NewHandler(
		sessionManager,
		voiceRouter,
		voiceauth.NewValidator(jwtManager),
		logger,
		metrics,
		ctrl,
	)

	pool := &ServerPool{
		conns:      make([]*net.UDPConn, 0, count),
		ports:      make([]int, 0, count),
		portToConn: make(map[int]*net.UDPConn),
		handler:    handler,
		logger:     logger,
		metrics:    metrics,
		workChan:   make(chan *poolJob, workChanSize),
		stopChan:   make(chan struct{}),
		packetPool: newPacketPool(),
	}

	for i := 0; i < count; i++ {
		port := startPort + i
		if singlePort {
			port = startPort
		}
		conn, err := listenUDP(host, port, singlePort)
		if err != nil {
			pool.closeAll()
			return nil, fmt.Errorf("listen port %d: %w", port, err)
		}

		_ = conn.SetReadBuffer(4 * 1024 * 1024)
		_ = conn.SetWriteBuffer(4 * 1024 * 1024)

		pool.conns = append(pool.conns, conn)
		if !singlePort {
			pool.ports = append(pool.ports, port)
			pool.portToConn[port] = conn
		}
	}
	if singlePort && len(pool.conns) > 0 {
		pool.ports = append(pool.ports, startPort)
		pool.portToConn[startPort] = pool.conns[0]
	}

	logger.Info("UDP pool created",
		zap.Int("port_start", startPort),
		zap.Int("count", count),
	)

	return pool, nil
}

// Ports returns the bound ports in per-port mode (a single entry in single-port
// mode). The slice is shared, not copied; callers must not mutate it.
func (p *ServerPool) Ports() []int {
	return p.ports
}

// ConnForPort returns the socket bound to port, or nil if no socket owns it. Used
// to reply from the exact ingress port a client was assigned.
func (p *ServerPool) ConnForPort(port int) *net.UDPConn {
	return p.portToConn[port]
}

// PrimaryConn returns the pool's first bound socket. It is the default egress
// for media that arrived over the TCP fallback (no origin UDP socket) and must
// still reach UDP peers. Bound in the constructor, so it is valid immediately.
func (p *ServerPool) PrimaryConn() *net.UDPConn {
	if len(p.conns) == 0 {
		return nil
	}
	return p.conns[0]
}

// Handler exposes the shared packet handler so alternate transports (e.g. the
// TCP fallback) can dispatch frames through the same processing path.
func (p *ServerPool) Handler() *Handler {
	return p.handler
}

// SweepInactive runs the two-stage inactivity sweep, broadcasting ParticipantLeft
// from the pool's primary socket. Returns the IDs of fully-removed sessions.
func (p *ServerPool) SweepInactive(inactiveAfter, removeAfter time.Duration) []uint32 {
	if len(p.conns) == 0 {
		return nil
	}
	return p.handler.SweepAndNotify(p.conns[0], inactiveAfter, removeAfter)
}

// Start launches the worker pool (NumCPU*2, min 4) plus one read loop per socket,
// then blocks until ctx is cancelled. On cancel it signals stop, closes the
// sockets (unblocking the read loops), waits for every goroutine, and returns nil.
func (p *ServerPool) Start(ctx context.Context) error {
	numWorkers := runtime.NumCPU() * 2
	if numWorkers < 4 {
		numWorkers = 4
	}

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

// closeAll closes every bound socket, ignoring errors. Used both on a failed
// constructor and on shutdown to unblock the read loops.
func (p *ServerPool) closeAll() {
	for _, conn := range p.conns {
		_ = conn.Close()
	}
}

// listenUDP opens a UDP socket. In single-port mode it sets SO_REUSEPORT so many
// sockets can share one public port; otherwise it does an ordinary bind.
func listenUDP(host string, port int, singlePort bool) (*net.UDPConn, error) {
	addrStr := fmt.Sprintf("%s:%d", host, port)
	if !singlePort {
		addr, err := net.ResolveUDPAddr("udp", addrStr)
		if err != nil {
			return nil, err
		}
		return net.ListenUDP("udp", addr)
	}
	if !reusePortSupported() {
		return nil, fmt.Errorf("single-port mode requires SO_REUSEPORT (Linux only)")
	}
	lc := net.ListenConfig{Control: reusePortControl}
	pc, err := lc.ListenPacket(context.Background(), "udp", addrStr)
	if err != nil {
		return nil, err
	}
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("expected *net.UDPConn, got %T", pc)
	}
	return uc, nil
}

// readLoop reads datagrams from conn into pooled buffers and hands each to a
// worker via workChan. Oversized (> maxPacketLen) reads are dropped. A read error
// exits only when stopChan is closed; otherwise it Releases the buffer and retries.
// If workChan is full the packet is dropped (metric recorded) rather than blocking
// the socket.
func (p *ServerPool) readLoop(conn *net.UDPConn) {
	defer p.wg.Done()

	for {
		pkt := p.packetPool.Get().(*packetBuffer)
		buf := pkt.PrepareForRead()

		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			pkt.Release()
			select {
			case <-p.stopChan:
				return
			default:
				continue
			}
		}

		if n > maxPacketLen {
			pkt.Release()
			continue
		}

		pkt.SetLen(n)

		select {
		case p.workChan <- &poolJob{pkt: pkt, addr: addr, conn: conn}:
		default:
			if p.metrics != nil {
				p.metrics.RecordPacketDropped()
			}
			pkt.Release()
		}
	}
}

// worker pulls jobs off workChan and dispatches each through the handler, passing
// the buffer as the owner so the router can Retain it for fan-out. It Releases the
// read loop's own reference once handling returns, exiting on stopChan.
func (p *ServerPool) worker() {
	defer p.wg.Done()

	for {
		select {
		case job := <-p.workChan:
			if job != nil {
				p.handler.HandlePacketOwned(job.pkt.Bytes(), job.pkt, job.addr, job.conn)
				job.pkt.Release()
			}
		case <-p.stopChan:
			return
		}
	}
}
