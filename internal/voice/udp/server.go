package udp

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	voiceauth "github.com/Alexander-D-Karpov/concord/internal/voice/auth"
	"github.com/Alexander-D-Karpov/concord/internal/voice/router"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

type Server struct {
	conn     *net.UDPConn
	handler  *Handler
	logger   *zap.Logger
	metrics  *telemetry.Metrics
	stopChan chan struct{}
	wg       sync.WaitGroup

	packetPool sync.Pool
	workChan   chan *packetJob
}

type packetJob struct {
	data []byte
	addr *net.UDPAddr
}

const (
	numWorkers   = 4
	workChanSize = 10000
	maxPacketLen = 1500
)

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
	)

	return &Server{
		conn:     conn,
		handler:  handler,
		logger:   logger,
		metrics:  metrics,
		stopChan: make(chan struct{}),
		workChan: make(chan *packetJob, workChanSize),
		packetPool: sync.Pool{
			New: func() interface{} {
				b := make([]byte, maxPacketLen)
				return &b
			},
		},
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	s.logger.Info("UDP server starting", zap.String("address", s.conn.LocalAddr().String()))

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

func (s *Server) readLoop() {
	defer s.wg.Done()

	for {
		bufPtr := s.packetPool.Get().(*[]byte)
		buf := *bufPtr

		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			s.packetPool.Put(bufPtr)
			select {
			case <-s.stopChan:
				return
			default:
				continue
			}
		}

		if n > maxPacketLen {
			s.packetPool.Put(bufPtr)
			continue
		}

		data := make([]byte, n)
		copy(data, buf[:n])
		s.packetPool.Put(bufPtr)

		select {
		case s.workChan <- &packetJob{data: data, addr: addr}:
		default:
			if s.metrics != nil {
				s.metrics.RecordPacketDropped()
			}
		}
	}
}

func (s *Server) worker() {
	defer s.wg.Done()

	for {
		select {
		case job := <-s.workChan:
			if job != nil {
				s.handler.HandlePacket(job.data, job.addr, s.conn)
			}
		case <-s.stopChan:
			return
		}
	}
}
