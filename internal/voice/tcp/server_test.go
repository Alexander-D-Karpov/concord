package tcp

import (
	"encoding/binary"
	"io"
	"net"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
)

// tcpTransport must satisfy session.Transport and frame packets as
// [uint32 len][payload].
func TestTCPTransportFraming(t *testing.T) {
	var _ session.Transport = (*tcpTransport)(nil)

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	tp := &tcpTransport{conn: c1}
	payload := []byte("hello-voice-frame")
	go func() { _ = tp.WritePacket(payload) }()

	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c2, hdr); err != nil {
		t.Fatalf("read len: %v", err)
	}
	n := binary.BigEndian.Uint32(hdr)
	if int(n) != len(payload) {
		t.Fatalf("framed length %d != %d", n, len(payload))
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c2, buf); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("payload mismatch: %q", buf)
	}
}

func TestSynthUDPAddrFromTCP(t *testing.T) {
	a := synthUDPAddr(&net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 40000})
	if a.String() != "10.0.0.5:40000" {
		t.Fatalf("unexpected synth addr: %s", a.String())
	}
}
