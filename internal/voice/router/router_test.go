package router

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/voice/congestion"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"go.uber.org/zap"
)

// enqueue must place packets into their kind's lane so the worker can drain
// control before audio before video.
func TestRouterEnqueuesByKind(t *testing.T) {
	r := &Router{
		numWorkers:    1,
		controlQueues: []chan *sendTask{make(chan *sendTask, 4)},
		audioQueues:   []chan *sendTask{make(chan *sendTask, 4)},
		videoQueues:   []chan *sendTask{make(chan *sendTask, 4)},
		stopChan:      make(chan struct{}),
		sendTaskPool:  sync.Pool{New: func() interface{} { return new(sendTask) }},
	}
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}

	if !r.enqueue(0, []byte("a"), nil, addr, nil, nil, qAudio) {
		t.Fatal("audio enqueue failed")
	}
	if !r.enqueue(0, []byte("v"), nil, addr, nil, nil, qVideo) {
		t.Fatal("video enqueue failed")
	}
	if !r.enqueue(0, []byte("c"), nil, addr, nil, nil, qControl) {
		t.Fatal("control enqueue failed")
	}

	if len(r.audioQueues[0]) != 1 {
		t.Fatalf("audio lane has %d, want 1", len(r.audioQueues[0]))
	}
	if len(r.videoQueues[0]) != 1 {
		t.Fatalf("video lane has %d, want 1", len(r.videoQueues[0]))
	}
	if len(r.controlQueues[0]) != 1 {
		t.Fatalf("control lane has %d, want 1", len(r.controlQueues[0]))
	}
}

// A packet that arrived over the TCP fallback has no origin socket and no
// egress transport; the worker must still reach a UDP peer by falling back to
// the router's default socket. Without the fallback such packets are silently
// dropped (TCP sender is mute toward every UDP participant).
func TestSendWorkerFallsBackToDefaultConnForTCPOrigin(t *testing.T) {
	recv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()
	egress, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = egress.Close() }()

	r := &Router{
		logger:        zap.NewNop(),
		numWorkers:    1,
		controlQueues: []chan *sendTask{make(chan *sendTask, 4)},
		audioQueues:   []chan *sendTask{make(chan *sendTask, 4)},
		videoQueues:   []chan *sendTask{make(chan *sendTask, 4)},
		stopChan:      make(chan struct{}),
		sendTaskPool:  sync.Pool{New: func() interface{} { return new(sendTask) }},
	}
	r.SetDefaultConn(egress)
	r.wg.Add(1)
	go r.sendWorker(r.controlQueues[0], r.audioQueues[0], r.videoQueues[0])
	defer func() { close(r.stopChan); r.wg.Wait() }()

	payload := []byte("tcp-origin-media")
	dst := recv.LocalAddr().(*net.UDPAddr)
	// nil conn + nil transport == a packet that arrived over the TCP fallback.
	if !r.enqueue(0, payload, nil, dst, nil, nil, qAudio) {
		t.Fatal("enqueue dropped")
	}

	_ = recv.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, _, err := recv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("TCP-origin media not delivered to UDP peer via default conn: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Fatalf("payload mismatch: got %q, want %q", buf[:n], payload)
	}
}

// Without a QualityPref, a single video stream tagged with a varying Layer byte
// must forward EVERY packet — the layer-selection path is opt-in, and dropping
// non-"target" layers of one stream would leave the receiver with nothing to
// decode (the bug this guards against).
func TestRouteMediaForwardsAllVideoLayersWithoutPref(t *testing.T) {
	recv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()
	egress, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = egress.Close() }()

	mgr := session.NewManager()
	sender := mgr.CreateSession("A", "room-1", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000}, nil, true, false)
	dst := mgr.CreateSession("B", "room-1", recv.LocalAddr().(*net.UDPAddr), nil, true, false)
	dst.UpdateSubscriptions([]uint32{sender.VideoSSRC})

	r := NewRouter(mgr, zap.NewNop(), nil, congestion.NewController(congestion.DefaultConfig()))
	defer r.Stop()
	r.SetDefaultConn(egress) // conn==nil path (TCP-origin style) reaches the UDP peer

	// Same video SSRC, two different Layer tags, no QualityPref on the receiver.
	for _, layer := range []uint8{2, 1} {
		hdr := protocol.MediaHeader{Type: protocol.PacketTypeVideo, SSRC: sender.VideoSSRC, Layer: layer}
		r.RouteMediaRaw(hdr, []byte{layer, 'x'}, sender.GetAddr(), nil)
	}

	got := map[byte]bool{}
	_ = recv.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	for i := 0; i < 2; i++ {
		n, _, err := recv.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("expected 2 forwarded video packets, got %d: %v", i, err)
		}
		if n > 0 {
			got[buf[0]] = true
		}
	}
	if !got[1] || !got[2] {
		t.Fatalf("both layers must forward without a pref; got %v", got)
	}
}
