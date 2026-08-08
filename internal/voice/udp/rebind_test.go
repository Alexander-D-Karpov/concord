package udp

import (
	"net"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	voiceauth "github.com/Alexander-D-Karpov/concord/internal/voice/auth"
	"github.com/Alexander-D-Karpov/concord/internal/voice/congestion"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	"github.com/Alexander-D-Karpov/concord/internal/voice/router"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

// newTestHandler wires a Handler against real (non-mock) session/router/telemetry
// collaborators, matching how cmd/concord-voice constructs it, so this test
// exercises the actual mobile-reconnect contract rather than a stub.
func newTestHandler(t *testing.T) (*Handler, *session.Manager, *jwt.Manager) {
	t.Helper()
	sm := session.NewManager()
	jm := jwt.NewManager("test-secret", "voice-secret")
	ctrl := congestion.NewController(congestion.DefaultConfig())
	m := telemetry.NewMetrics(zap.NewNop())
	r := router.NewRouter(sm, zap.NewNop(), m, ctrl)
	h := NewHandler(sm, r, voiceauth.NewValidator(jm), zap.NewNop(), m, ctrl)
	return h, sm, jm
}

func helloPacket(t *testing.T, token string) []byte {
	t.Helper()
	data, err := protocol.BuildJSONPacket(protocol.PacketTypeHello, protocol.HelloPayload{
		Token:    token,
		Protocol: protocol.ProtocolVersion,
		Codec:    "opus",
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestRepeatHelloRebindsSameSession: a second Hello from the same (user,room) on a
// NEW address rebinds the existing session (same SSRC, no duplicate, rebind metric
// incremented) rather than creating a second participant.
func TestRepeatHelloRebindsSameSession(t *testing.T) {
	h, sm, jm := newTestHandler(t)
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	tok, err := jm.GenerateVoiceToken("user-1", "room-1", "srv-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	addr1 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40001}
	addr2 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40002}

	h.HandlePacket(helloPacket(t, tok), addr1, conn)
	s1 := sm.GetSessionByUserInRoom("user-1", "room-1")
	if s1 == nil {
		t.Fatal("expected a session after first Hello")
	}
	ssrc := s1.SSRC
	if got := len(sm.GetAllSessions()); got != 1 {
		t.Fatalf("expected 1 session, got %d", got)
	}

	h.HandlePacket(helloPacket(t, tok), addr2, conn)
	if got := len(sm.GetAllSessions()); got != 1 {
		t.Fatalf("rebind must not create a duplicate participant, got %d sessions", got)
	}
	s2 := sm.GetSessionByUserInRoom("user-1", "room-1")
	if s2 == nil || s2.SSRC != ssrc {
		t.Fatalf("rebind must preserve SSRC: was %d now %v", ssrc, s2)
	}
	if h.metrics.Rebinds.Load() == 0 {
		t.Error("expected the rebind to be counted (Rebinds metric)")
	}
}

// TestInvalidTokenHelloRejected: a Hello with a bad token creates no session.
func TestInvalidTokenHelloRejected(t *testing.T) {
	h, sm, _ := newTestHandler(t)
	conn, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer func() { _ = conn.Close() }()

	h.HandlePacket(helloPacket(t, "not-a-valid-token"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40003}, conn)
	if got := len(sm.GetAllSessions()); got != 0 {
		t.Fatalf("expected no session for an invalid token, got %d", got)
	}
}

// TestEvictionWindowExceedsClientReconnect: a session idle 30s survives; only well
// past the ~90s window is it removed — proving eviction grace >> client reconnect.
func TestEvictionWindowExceedsClientReconnect(t *testing.T) {
	h, sm, jm := newTestHandler(t)
	conn, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer func() { _ = conn.Close() }()

	tok, _ := jm.GenerateVoiceToken("user-2", "room-2", "srv-1", time.Hour)
	h.HandlePacket(helloPacket(t, tok), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40004}, conn)

	base := time.Now()
	sm.SweepInactive(20*time.Second, 90*time.Second, base.Add(30*time.Second))
	if sm.GetSessionByUserInRoom("user-2", "room-2") == nil {
		t.Fatal("session must survive a 30s gap (mobile handoff), but was evicted")
	}
	sm.SweepInactive(20*time.Second, 90*time.Second, base.Add(100*time.Second))
	if sm.GetSessionByUserInRoom("user-2", "room-2") != nil {
		t.Error("session should be removed after the eviction window")
	}
}
