// Package tcp provides an optional TCP/TLS fallback transport for voice clients
// whose UDP is blocked. Frames are length-prefixed ([uint32 len][payload]) and
// carry the exact same packet format as the UDP data plane; the shared
// udp.Handler treats a TCP-backed session identically, only writing replies onto
// the stream instead of the socket.
package tcp

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/udp"
	"go.uber.org/zap"
)

// maxFrameSize caps a single length-prefixed frame at 64 KiB; a larger declared
// length is treated as a protocol error and drops the connection.
const maxFrameSize = 64 * 1024

// tcpTransport implements session.Transport by length-prefix framing packets
// onto a TCP (optionally TLS) stream. Writes are serialized so concurrent
// forwarders don't interleave frames.
type tcpTransport struct {
	conn net.Conn
	mu   sync.Mutex
}

// WritePacket frames data with a 4-byte big-endian length prefix and writes it
// to the connection, holding the transport mutex so concurrent forwarders never
// interleave the header and payload of different packets.
func (t *tcpTransport) WritePacket(data []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err := t.conn.Write(data)
	return err
}

// Server is the TCP/TLS fallback listener. It reuses the shared udp.Handler for
// all packet logic, so a TCP-backed session behaves identically to a UDP one.
// tlsConfig is nil for plain TCP.
type Server struct {
	handler   *udp.Handler
	sessions  *session.Manager
	logger    *zap.Logger
	tlsConfig *tls.Config
}

// NewServer builds the fallback server. Pass a non-nil tlsConfig to listen with
// TLS; nil listens on plain TCP.
func NewServer(handler *udp.Handler, sessions *session.Manager, logger *zap.Logger, tlsConfig *tls.Config) *Server {
	return &Server{handler: handler, sessions: sessions, logger: logger, tlsConfig: tlsConfig}
}

// Start listens on port (TLS if configured) and serves each accepted connection
// in its own goroutine until ctx is cancelled, which closes the listener and
// returns nil. Returns an error only if the initial listen fails; transient
// accept errors are logged and retried.
func (s *Server) Start(ctx context.Context, port int) error {
	addr := fmt.Sprintf(":%d", port)
	var (
		ln  net.Listener
		err error
	)
	if s.tlsConfig != nil {
		ln, err = tls.Listen("tcp", addr, s.tlsConfig)
	} else {
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("tcp listen %s: %w", addr, err)
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	s.logger.Info("voice TCP fallback listening", zap.Int("port", port), zap.Bool("tls", s.tlsConfig != nil))

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				s.logger.Debug("tcp accept error", zap.Error(err))
				continue
			}
		}
		go s.handleConn(ctx, conn)
	}
}

// handleConn reads length-prefixed frames off conn and dispatches each to the
// shared handler, using a synthetic UDP address derived from the remote as the
// session key. It exits on EOF, a malformed frame length, or ctx cancellation,
// and on exit removes any session bound to this connection so a dropped TCP
// client is torn down promptly.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	tp := &tcpTransport{conn: conn}
	synthAddr := synthUDPAddr(conn.RemoteAddr())

	hdr := make([]byte, 4)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := io.ReadFull(conn, hdr); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(hdr)
		if n == 0 || n > maxFrameSize {
			s.logger.Debug("tcp bad frame length", zap.Uint32("len", n))
			break
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(conn, buf); err != nil {
			break
		}
		s.handler.HandleFramedTCP(buf, synthAddr, tp)
	}

	// On disconnect, drop the session bound to this connection.
	if sess := s.sessions.GetByAddr(synthAddr); sess != nil {
		s.sessions.RemoveSession(sess.ID)
	}
}

// synthUDPAddr derives a stable session-index key from a TCP remote address.
func synthUDPAddr(remote net.Addr) *net.UDPAddr {
	if t, ok := remote.(*net.TCPAddr); ok {
		return &net.UDPAddr{IP: t.IP, Port: t.Port, Zone: t.Zone}
	}
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}
