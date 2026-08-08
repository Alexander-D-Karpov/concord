package telemetry

import (
	"runtime"
	"sync"
	"time"
)

// LoadSampler turns cumulative counters into rates for heartbeats: real CPU
// utilization (0..1 of total capacity) and egress in Mbps, both measured as
// deltas between successive Sample calls.
type LoadSampler struct {
	mu        sync.Mutex
	inited    bool
	lastTime  time.Time
	lastCPU   float64 // process cpu-seconds
	lastBytes uint64
	numCPU    float64
}

// NewLoadSampler returns a sampler primed with the host's CPU count, used to
// normalize CPU-seconds into a 0..1 utilization fraction. The returned sampler
// is uninitialized until its first Sample call, which sets the baseline.
func NewLoadSampler() *LoadSampler {
	return &LoadSampler{numCPU: float64(runtime.NumCPU())}
}

// Sample returns CPU utilization (0..1) and egress rate (Mbps) since the
// previous call. The first call establishes a baseline and returns zeros.
func (l *LoadSampler) Sample(bytesSent uint64, now time.Time) (cpuFrac, mbps float64) {
	cpuSecs := processCPUSeconds()

	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.inited {
		l.inited = true
		l.lastTime = now
		l.lastCPU = cpuSecs
		l.lastBytes = bytesSent
		return 0, 0
	}

	dt := now.Sub(l.lastTime).Seconds()
	if dt <= 0 {
		return 0, 0
	}

	dCPU := cpuSecs - l.lastCPU
	var dBytes float64
	if bytesSent >= l.lastBytes {
		dBytes = float64(bytesSent - l.lastBytes)
	}

	l.lastTime = now
	l.lastCPU = cpuSecs
	l.lastBytes = bytesSent

	if l.numCPU > 0 {
		cpuFrac = (dCPU / dt) / l.numCPU
	}
	if cpuFrac < 0 {
		cpuFrac = 0
	}
	if cpuFrac > 1 {
		cpuFrac = 1
	}
	mbps = (dBytes * 8) / (dt * 1_000_000)
	return cpuFrac, mbps
}
