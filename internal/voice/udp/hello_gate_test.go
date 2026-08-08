package udp

import (
	"testing"
	"time"
)

func TestHelloGateThrottles(t *testing.T) {
	g := newHelloGate(5, 3) // 5/sec, burst 3
	t0 := time.Unix(1_700_000_000, 0)

	for i := 0; i < 3; i++ {
		if !g.allow("1.2.3.4", t0) {
			t.Fatalf("burst packet %d should be allowed", i)
		}
	}
	if g.allow("1.2.3.4", t0) {
		t.Fatal("4th hello in the same instant should be throttled")
	}
	if !g.allow("5.6.7.8", t0) {
		t.Fatal("a different IP has its own bucket")
	}
	if !g.allow("1.2.3.4", t0.Add(time.Second)) {
		t.Fatal("tokens should refill after 1s")
	}
}

func TestHelloGatePrune(t *testing.T) {
	g := newHelloGate(5, 3)
	t0 := time.Unix(1_700_000_000, 0)
	g.allow("1.2.3.4", t0)
	g.prune(t0.Add(10*time.Minute), 5*time.Minute)
	g.mu.Lock()
	n := len(g.buckets)
	g.mu.Unlock()
	if n != 0 {
		t.Fatalf("stale bucket not pruned: %d remain", n)
	}
}
