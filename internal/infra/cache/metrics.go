package cache

import (
	"sync/atomic"
)

// Metrics tracks cache hit/miss counts. Both counters are atomic, so all methods
// are safe for concurrent use without external locking.
type Metrics struct {
	hits   atomic.Uint64
	misses atomic.Uint64
}

// NewMetrics returns a zeroed Metrics.
func NewMetrics() *Metrics {
	return &Metrics{}
}

// RecordHit atomically increments the hit counter.
func (m *Metrics) RecordHit() {
	m.hits.Add(1)
}

// RecordMiss atomically increments the miss counter.
func (m *Metrics) RecordMiss() {
	m.misses.Add(1)
}

// GetStats returns the current hit and miss counts and the hit rate
// hits/(hits+misses). The rate is 0 when no lookups have been recorded (avoids
// divide-by-zero). The three reads are not one atomic snapshot.
func (m *Metrics) GetStats() (hits, misses uint64, hitRate float64) {
	h := m.hits.Load()
	miss := m.misses.Load()
	total := h + miss

	if total == 0 {
		return h, miss, 0.0
	}

	return h, miss, float64(h) / float64(total)
}

// Reset zeroes both counters.
func (m *Metrics) Reset() {
	m.hits.Store(0)
	m.misses.Store(0)
}
