package udp

import (
	"sync"
	"time"
)

const (
	// helloRatePerSec is the steady-state HELLO allowance per source IP (tokens/sec).
	helloRatePerSec = 5.0
	// helloBurst is the token-bucket depth: the most HELLOs one IP may send back to
	// back before being throttled to helloRatePerSec.
	helloBurst = 10.0
)

// helloGate is a per-source-IP token-bucket rate limiter applied to HELLO
// packets before the (expensive) JWT validation, to shed handshake floods.
type helloGate struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	rate    float64 // tokens/sec
	burst   float64
}

// ipBucket is one source IP's token bucket: tokens available and the time they
// were last refilled (used to lazily add rate*elapsed on the next allow).
type ipBucket struct {
	tokens   float64
	lastSeen time.Time
}

// newHelloGate builds a gate with the given steady rate (tokens/sec) and burst
// depth.
func newHelloGate(ratePerSec, burst float64) *helloGate {
	return &helloGate{buckets: make(map[string]*ipBucket), rate: ratePerSec, burst: burst}
}

// allow reports whether a HELLO from ip is permitted now, consuming a token.
func (g *helloGate) allow(ip string, now time.Time) bool {
	if ip == "" {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.buckets[ip]
	if b == nil {
		g.buckets[ip] = &ipBucket{tokens: g.burst - 1, lastSeen: now}
		return true
	}
	if elapsed := now.Sub(b.lastSeen).Seconds(); elapsed > 0 {
		b.tokens += elapsed * g.rate
		if b.tokens > g.burst {
			b.tokens = g.burst
		}
		b.lastSeen = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// prune drops buckets untouched for longer than maxIdle so the map does not grow
// unboundedly with churning source IPs. Called periodically from the sweep.
func (g *helloGate) prune(now time.Time, maxIdle time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for ip, b := range g.buckets {
		if now.Sub(b.lastSeen) > maxIdle {
			delete(g.buckets, ip)
		}
	}
}
