package voiceassign

import (
	"context"
	"testing"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
)

// expires_in is the voice token TTL in seconds; the client turns proactive
// refresh into its primary path off this value.
func TestVoiceTokenTTLIs300s(t *testing.T) {
	if got := int(voiceTokenTTL / time.Second); got != 300 {
		t.Fatalf("expires_in should be 300s, got %d", got)
	}
}

// tcp_endpoint is populated only when TCP fallback is configured, reusing the
// server's UDP host.
func TestTCPEndpointForGating(t *testing.T) {
	s := &Service{}
	if s.tcpEndpointFor("h") != (UDPEndpoint{}) {
		t.Fatal("TCP disabled (port 0) must yield an empty endpoint")
	}
	s.tcpPort = 8443
	if e := s.tcpEndpointFor("1.2.3.4"); e.Host != "1.2.3.4" || e.Port != 8443 {
		t.Fatalf("want 1.2.3.4:8443, got %+v", e)
	}
}

type fakePublisher struct{ events []*streamv1.ServerEvent }

func (f *fakePublisher) BroadcastToUser(_ string, e *streamv1.ServerEvent) {
	f.events = append(f.events, e)
}

func newTestStore() *sessionStore {
	return &sessionStore{
		byRoom:     make(map[string]map[string]*VoiceSession),
		byUser:     make(map[string]*VoiceSession),
		roomCrypto: make(map[string]CryptoSuite),
		roomPort:   make(map[string]int),
		roomServer: make(map[string]string),
		portCount:  50,
	}
}

// A same-room re-join (same pinned server) must keep the same UDP port so an
// in-progress call is not disrupted by a token refresh.
func TestAssignPortIdempotentForSameRoomServer(t *testing.T) {
	ss := newTestStore()
	p1 := ss.assignPort("room-1", "server-a", 50000)
	p2 := ss.assignPort("room-1", "server-a", 50000)
	if p1 != p2 {
		t.Fatalf("port not stable across re-join: %d vs %d", p1, p2)
	}
	if p1 < 50000 || p1 >= 50000+ss.portCount {
		t.Fatalf("port %d out of range", p1)
	}
}

func TestAssignPortRehashesOnServerChange(t *testing.T) {
	ss := newTestStore()
	_ = ss.assignPort("room-1", "server-a", 50000)
	p := ss.assignPort("room-1", "server-b", 60000)
	if ss.roomServer["room-1"] != "server-b" {
		t.Fatal("server pin not updated on server change")
	}
	if p < 60000 || p >= 60000+ss.portCount {
		t.Fatalf("port %d out of new base range", p)
	}
}

// Room crypto must be stable across re-joins so all participants keep sharing
// one key (the in-memory path, cache == nil).
func TestGetOrCreateRoomCryptoIdempotent(t *testing.T) {
	s := &Service{sessions: newTestStore()}
	cs1, err := s.getOrCreateRoomCrypto(context.Background(), "room-1")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	cs2, err := s.getOrCreateRoomCrypto(context.Background(), "room-1")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if cs1.AEAD != cs2.AEAD ||
		string(cs1.KeyMaterial) != string(cs2.KeyMaterial) ||
		string(cs1.KeyID) != string(cs2.KeyID) ||
		string(cs1.NonceBase) != string(cs2.NonceBase) {
		t.Fatal("room crypto changed across re-join (must be stable)")
	}
	if !validCrypto(cs1) {
		t.Fatal("generated crypto is invalid")
	}
}

// When a voice server goes offline, affected users must receive a
// VoiceServerChanged event so their clients auto-rejoin.
func TestClearSessionsForServerPublishesVoiceServerChanged(t *testing.T) {
	pub := &fakePublisher{}
	s := &Service{sessions: newTestStore(), pub: pub}
	s.sessions.add(&VoiceSession{UserID: "u1", RoomID: "room-1", ServerID: "dead-server"})

	s.clearSessionsForServer(context.Background(), "dead-server")

	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
	vsc := pub.events[0].GetVoiceServerChanged()
	if vsc == nil || vsc.UserId != "u1" || vsc.RoomId != "room-1" || vsc.Reason != "server_offline" {
		t.Fatalf("wrong event payload: %+v", vsc)
	}
	if _, ok := s.sessions.byUser["u1"]; ok {
		t.Fatal("session was not cleared")
	}
}

// On membership change the room key must rotate (new material, incremented wire
// KeyID) and be pushed to remaining members so a departed user loses access.
func TestRotateRoomKeyPublishesAndChangesKey(t *testing.T) {
	pub := &fakePublisher{}
	s := &Service{sessions: newTestStore(), pub: pub}
	orig, err := generateCryptoSuite()
	if err != nil {
		t.Fatal(err)
	}
	s.sessions.roomCrypto["room-1"] = orig
	s.sessions.add(&VoiceSession{UserID: "u1", RoomID: "room-1", ServerID: "srv"})

	if err := s.RotateRoomKey(context.Background(), "room-1"); err != nil {
		t.Fatal(err)
	}
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
	vkr := pub.events[0].GetVoiceKeyRotated()
	if vkr == nil || vkr.RoomId != "room-1" || len(vkr.KeyMaterial) != 32 {
		t.Fatalf("bad rotation event: %+v", vkr)
	}
	if string(vkr.KeyMaterial) == string(orig.KeyMaterial) {
		t.Fatal("key material must change on rotation")
	}
	if vkr.KeyId[0] != orig.KeyID[0]+1 {
		t.Fatalf("wire key id byte should increment: %d vs %d", vkr.KeyId[0], orig.KeyID[0])
	}
	if string(s.sessions.roomCrypto["room-1"].KeyMaterial) != string(vkr.KeyMaterial) {
		t.Fatal("stored suite not updated to the new key")
	}
}
