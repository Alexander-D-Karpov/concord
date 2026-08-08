package udp

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	voiceauth "github.com/Alexander-D-Karpov/concord/internal/voice/auth"
	"github.com/Alexander-D-Karpov/concord/internal/voice/router"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

// Server is the legacy single-socket UDP data plane. It mirrors ServerPool but
// binds one socket and wires a Handler with a nil congestion controller, so
// congestion control is disabled on this path. Prefer ServerPool for production.
type Server struct {
	conn     *net.UDPConn
	handler  *Handler
	logger   *zap.Logger
	metrics  *telemetry.Metrics
	stopChan chan struct{}
	wg       sync.WaitGroup

	packetPool *sync.Pool
	workChan   chan *packetJob
}

// packetJob carries a received datagram (pooled buffer + source address) from the
// read loop to a worker. The reply socket is the server's single conn, so it is
// not carried here.
type packetJob struct {
	pkt  *packetBuffer
	addr *net.UDPAddr
}

const (
	// workChanSize bounds the in-flight backlog between read loop and workers;
	// beyond it, datagrams are dropped rather than queued unboundedly.
	workChanSize = 10000
	// maxPacketLen is the largest datagram accepted; larger reads are discarded and
	// each pooled buffer is sized to it.
	maxPacketLen = 1500
)

// NewServer binds a single UDP socket at host:port, sets 8 MiB socket buffers
// (warns but continues if that fails), and builds a Handler with no congestion
// controller. Returns an error if resolving or binding fails. Call Start to run.
func NewServer(
	host string,
	port int,
	sessionManager *session.Manager,
	voiceRouter *router.Router,
	jwtManager *jwt.Manager,
	logger *zap.Logger,
	metrics *telemetry.Metrics,
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

	handler := NewHandler(
		sessionManager,
		voiceRouter,
		voiceauth.NewValidator(jwtManager),
		logger,
		metrics,
		nil,
	)

	return &Server{
		conn:       conn,
		handler:    handler,
		logger:     logger,
		metrics:    metrics,
		stopChan:   make(chan struct{}),
		workChan:   make(chan *packetJob, workChanSize),
		packetPool: newPacketPool(),
	}, nil
}

// Start launches the worker pool (NumCPU*2, min 4) and the single read loop, then
// blocks until ctx is cancelled, whereupon it stops, closes the socket, waits for
// all goroutines, and returns nil.
func (s *Server) Start(ctx context.Context) error {
	s.logger.Info("UDP server starting", zap.String("address", s.conn.LocalAddr().String()))

	numWorkers := runtime.NumCPU() * 2
	if numWorkers < 4 {
		numWorkers = 4
	}

	for i := 0; i < numWorkers; i++ {
		s.wg.Add(1)
		go s.worker()
	}

	s.wg.Add(1)
	go s.readLoop()

	<-ctx.Done()
	close(s.stopChan)
	_ = s.conn.Close()
	s.wg.Wait()
	s.logger.Info("UDP server stopped")
	return nil
}

// readLoop reads datagrams into pooled buffers and dispatches them to workers via
// workChan, dropping oversized reads and (on a full queue) excess load. A read
// error exits only after stopChan closes; otherwise it Releases the buffer and
// continues.
func (s *Server) readLoop() {
	defer s.wg.Done()

	for {
		pkt := s.packetPool.Get().(*packetBuffer)
		buf := pkt.PrepareForRead()

		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			pkt.Release()
			select {
			case <-s.stopChan:
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
		case s.workChan <- &packetJob{pkt: pkt, addr: addr}:
		default:
			if s.metrics != nil {
				s.metrics.RecordPacketDropped()
			}
			pkt.Release()
		}
	}
}

// worker dispatches each job through the handler (passing the buffer as owner for
// fan-out) and Releases the read loop's reference afterward, exiting on stopChan.
func (s *Server) worker() {
	defer s.wg.Done()

	for {
		select {
		case job := <-s.workChan:
			if job != nil {
				s.handler.HandlePacketOwned(job.pkt.Bytes(), job.pkt, job.addr, s.conn)
				job.pkt.Release()
			}
		case <-s.stopChan:
			return
		}
	}
}
