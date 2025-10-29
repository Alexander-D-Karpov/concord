package qos

import (
	"sync"
	"time"
)

type BitrateAdapter struct {
	currentBitrate int
	targetBitrate  int
	minBitrate     int
	maxBitrate     int
	mu             sync.RWMutex
}

func NewBitrateAdapter(initialBitrate, minBitrate, maxBitrate int) *BitrateAdapter {
	return &BitrateAdapter{
		currentBitrate: initialBitrate,
		targetBitrate:  initialBitrate,
		minBitrate:     minBitrate,
		maxBitrate:     maxBitrate,
	}
}

func (ba *BitrateAdapter) AdjustBitrate(packetLoss float64, latency time.Duration) {
	ba.mu.Lock()
	defer ba.mu.Unlock()

	if packetLoss > 0.05 || latency > 200*time.Millisecond {
		ba.targetBitrate = int(float64(ba.currentBitrate) * 0.85)
	} else if packetLoss < 0.01 && latency < 50*time.Millisecond {
		ba.targetBitrate = int(float64(ba.currentBitrate) * 1.15)
	}

	if ba.targetBitrate < ba.minBitrate {
		ba.targetBitrate = ba.minBitrate
	}

	if ba.targetBitrate > ba.maxBitrate {
		ba.targetBitrate = ba.maxBitrate
	}

	ba.currentBitrate += (ba.targetBitrate - ba.currentBitrate) / 10
}

func (ba *BitrateAdapter) GetCurrentBitrate() int {
	ba.mu.RLock()
	defer ba.mu.RUnlock()
	return ba.currentBitrate
}
