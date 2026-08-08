//go:build linux

package udp

import (
	"context"
	"net"
	"testing"
)

// Two sockets binding the same UDP port must both succeed with SO_REUSEPORT.
func TestReusePortAllowsDuplicateBind(t *testing.T) {
	lc := net.ListenConfig{Control: reusePortControl}
	pc1, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	defer func() { _ = pc1.Close() }()

	addr := pc1.LocalAddr().String()
	pc2, err := lc.ListenPacket(context.Background(), "udp", addr)
	if err != nil {
		t.Fatalf("second bind to %s should succeed with SO_REUSEPORT: %v", addr, err)
	}
	defer func() { _ = pc2.Close() }()
}
