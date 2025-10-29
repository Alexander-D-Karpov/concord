package cache

import (
	"sync/atomic"
)

type Metrics struct {
	hits   atomic.Uint64
	misses atomic.Uint64
}

func NewMetrics() *Metrics {
	return &Metrics{}
}

func (m *Metrics) RecordHit() {
	m.hits.Add(1)
}

func (m *Metrics) RecordMiss() {
	m.misses.Add(1)
}

func (m *Metrics) GetStats() (hits, misses uint64, hitRate float64) {
	h := m.hits.Load()
	miss := m.misses.Load()
	total := h + miss

	if total == 0 {
		return h, miss, 0.0
	}

	return h, miss, float64(h) / float64(total)
}

func (m *Metrics) Reset() {
	m.hits.Store(0)
	m.misses.Store(0)
}
