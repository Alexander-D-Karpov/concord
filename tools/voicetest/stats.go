package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// stats holds the aggregate counters shared by all bots. Counters are atomic; the RTT
// sample slice is mutex-guarded. Read via summary() (plain-log mode) or field-by-field
// by the TUI.
type stats struct {
	audioSent    atomic.Uint64
	videoSent    atomic.Uint64
	audioRecv    atomic.Uint64
	videoRecv    atomic.Uint64
	pongRecv     atomic.Uint64
	welcomeOK    atomic.Uint64
	errors       atomic.Uint64
	bytesOut     atomic.Uint64
	bytesIn      atomic.Uint64
	bitrateHints atomic.Uint64
	lastBitrate  atomic.Uint64
	rttSamples   []time.Duration
	rttMu        sync.Mutex
}

// addRTT records one round-trip-time sample under the RTT mutex.
func (s *stats) addRTT(d time.Duration) {
	s.rttMu.Lock()
	s.rttSamples = append(s.rttSamples, d)
	s.rttMu.Unlock()
}

// summary returns a single-line human-readable digest of all counters plus RTT
// avg/min/max over the collected samples, for the plain-log and final reports.
func (s *stats) summary() string {
	s.rttMu.Lock()
	defer s.rttMu.Unlock()
	var avg, mn, mx time.Duration
	if n := len(s.rttSamples); n > 0 {
		mn = s.rttSamples[0]
		for _, r := range s.rttSamples {
			avg += r
			if r < mn {
				mn = r
			}
			if r > mx {
				mx = r
			}
		}
		avg /= time.Duration(n)
	}
	return fmt.Sprintf(
		"audio_tx=%d video_tx=%d audio_rx=%d video_rx=%d pongs=%d welcomes=%d hints=%d last_br=%d errs=%d out=%dKB in=%dKB rtt(avg=%v min=%v max=%v n=%d)",
		s.audioSent.Load(), s.videoSent.Load(),
		s.audioRecv.Load(), s.videoRecv.Load(),
		s.pongRecv.Load(), s.welcomeOK.Load(),
		s.bitrateHints.Load(), s.lastBitrate.Load(), s.errors.Load(),
		s.bytesOut.Load()/1024, s.bytesIn.Load()/1024,
		avg, mn, mx, len(s.rttSamples),
	)
}
