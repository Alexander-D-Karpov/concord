package session

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
)

func TestSessionCapabilities(t *testing.T) {
	s := &Session{}
	fec, dtx, mb := s.GetCapabilities()
	if fec || dtx || mb != 0 {
		t.Fatalf("defaults not zero: %v %v %d", fec, dtx, mb)
	}
	s.SetCapabilities(true, false, 900_000)
	fec, dtx, mb = s.GetCapabilities()
	if !fec || dtx || mb != 900_000 {
		t.Fatalf("caps not stored: %v %v %d", fec, dtx, mb)
	}
}

func TestRetransmitKeyframePinSurvivesTTL(t *testing.T) {
	rb := NewRetransmitBuffer(50 * time.Millisecond)
	t0 := time.Unix(1_700_000_000, 0)
	rb.storeAt(1, 100, true, []byte("kf"), t0) // keyframe frame ts=100

	late := t0.Add(500 * time.Millisecond)
	rb.storeAt(2, 100, true, []byte("kf2"), late) // same keyframe -> still pinned
	if rb.getAt(1, late) == nil {
		t.Fatal("pinned keyframe packet must survive past TTL")
	}
}

func TestRetransmitNewKeyframeUnpinsOld(t *testing.T) {
	rb := NewRetransmitBuffer(50 * time.Millisecond)
	t0 := time.Unix(1_700_000_000, 0)
	rb.storeAt(1, 100, true, []byte("kf-a"), t0)
	late := t0.Add(500 * time.Millisecond)
	rb.storeAt(2, 200, true, []byte("kf-b"), late) // new keyframe (different ts)
	if rb.getAt(1, late) != nil {
		t.Fatal("old keyframe should be unpinned and TTL-evicted")
	}
	if rb.getAt(2, late) == nil {
		t.Fatal("new keyframe must be retained")
	}
}

func TestRetransmitNonKeyframeEvictsOnTTL(t *testing.T) {
	rb := NewRetransmitBuffer(50 * time.Millisecond)
	t0 := time.Unix(1_700_000_000, 0)
	rb.storeAt(1, 100, false, []byte("p"), t0)
	if rb.getAt(1, t0.Add(10*time.Millisecond)) == nil {
		t.Fatal("fresh non-keyframe should be retrievable")
	}
	rb.storeAt(2, 101, false, []byte("q"), t0.Add(100*time.Millisecond)) // triggers eviction sweep
	if rb.getAt(1, t0.Add(100*time.Millisecond)) != nil {
		t.Fatal("stale non-keyframe should be evicted")
	}
}

func TestRetransmitKeyframePinBounded(t *testing.T) {
	rb := NewRetransmitBuffer(50 * time.Millisecond)
	t0 := time.Unix(1_700_000_000, 0)
	for i := 0; i < maxPinnedPackets+10; i++ {
		rb.storeAt(uint16(i), 100, true, []byte("x"), t0)
	}
	if rb.pinnedN > maxPinnedPackets {
		t.Fatalf("pinned count exceeded bound: %d", rb.pinnedN)
	}
}

func TestSweepInactiveTwoStage(t *testing.T) {
	m := NewManager()
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5000}
	sess := m.CreateSession("u1", "room-1", addr, nil, true, false)
	base := sess.LastActivity()

	ni, rm := m.SweepInactive(20*time.Second, 90*time.Second, base.Add(5*time.Second))
	if len(ni) != 0 || len(rm) != 0 {
		t.Fatalf("fresh session swept: inactive=%d removed=%d", len(ni), len(rm))
	}

	ni, rm = m.SweepInactive(20*time.Second, 90*time.Second, base.Add(30*time.Second))
	if len(ni) != 1 || len(rm) != 0 {
		t.Fatalf("expected 1 inactive/0 removed, got %d/%d", len(ni), len(rm))
	}
	if ni[0].UserID != "u1" || ni[0].RoomID != "room-1" || ni[0].SSRC != sess.SSRC {
		t.Fatalf("wrong inactive info: %+v", ni[0])
	}

	ni, _ = m.SweepInactive(20*time.Second, 90*time.Second, base.Add(40*time.Second))
	if len(ni) != 0 {
		t.Fatal("inactive announced more than once")
	}

	_, rm = m.SweepInactive(20*time.Second, 90*time.Second, base.Add(100*time.Second))
	if len(rm) != 1 {
		t.Fatalf("expected 1 removed, got %d", len(rm))
	}
	if m.GetSession(sess.ID) != nil {
		t.Fatal("session not removed from manager")
	}
}

func TestTouchReactivatesInactiveSession(t *testing.T) {
	m := NewManager()
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5001}
	sess := m.CreateSession("u2", "room-2", addr, nil, true, false)

	m.SweepInactive(20*time.Second, 90*time.Second, sess.LastActivity().Add(30*time.Second))
	if !sess.IsInactive() {
		t.Fatal("session should be inactive after sweep")
	}
	if !m.Touch(sess.ID) {
		t.Fatal("Touch should report reactivation")
	}
	if sess.IsInactive() {
		t.Fatal("session should be active after Touch")
	}
	if m.Touch(sess.ID) {
		t.Fatal("second Touch should not report reactivation")
	}
}

// During a key-rotation overlap the session must accept both the current and the
// immediately-previous key, selected by the wire KeyID byte.
func TestSessionDualKeyOverlap(t *testing.T) {
	sc1, err := crypto.NewSessionCryptoDerived(bytes.Repeat([]byte{1}, 32), "room", 5)
	if err != nil {
		t.Fatal(err)
	}
	sc2, err := crypto.NewSessionCryptoDerived(bytes.Repeat([]byte{2}, 32), "room", 6)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{Crypto: sc1}
	s.SetCrypto(sc2) // rotate: sc2 current (keyID 6), sc1 retained as prev (keyID 5)

	if s.CryptoForKeyID(6) != sc2 {
		t.Fatal("current key id should select the new cipher")
	}
	if s.CryptoForKeyID(5) != sc1 {
		t.Fatal("previous key id should be accepted during overlap")
	}
	if s.CryptoForKeyID(99) != sc2 {
		t.Fatal("unknown key id should fall back to current")
	}
}

// A fresh session receives all media (un-inited default), a premature/empty
// subscription update must NOT blacken it, and a real set narrows correctly
// without a later spurious empty wiping it.
func TestUpdateSubscriptionsIgnoresEmpty(t *testing.T) {
	s := &Session{}

	if !s.IsSubscribedTo(2001) {
		t.Fatal("fresh (un-inited) session must receive all media")
	}

	s.UpdateSubscriptions(nil) // premature/stale empty
	if !s.IsSubscribedTo(2001) {
		t.Fatal("empty subscription update must not narrow to nothing")
	}

	s.UpdateSubscriptions([]uint32{2000, 2001})
	if !s.IsSubscribedTo(2001) {
		t.Fatal("explicitly subscribed SSRC must forward")
	}
	if s.IsSubscribedTo(9999) {
		t.Fatal("unsubscribed SSRC must not forward once a real set applied")
	}

	s.UpdateSubscriptions([]uint32{}) // spurious empty after a real set
	if !s.IsSubscribedTo(2001) {
		t.Fatal("spurious empty must not wipe an applied subscription set")
	}
}
