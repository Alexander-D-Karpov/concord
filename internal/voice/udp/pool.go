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

type poolJob struct {
	pkt  *packetBuffer
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
	handler := NewHandler(
		sessionManager,
		voiceRouter,
		voiceauth.NewValidator(jwtManager),
		logger,
		metrics,
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

func (p *ServerPool) closeAll() {
	for _, conn := range p.conns {
		_ = conn.Close()
	}
}

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
