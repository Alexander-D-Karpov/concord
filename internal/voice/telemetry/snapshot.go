package telemetry

import (
	"fmt"
	"sort"
	"time"
)

// rawCounters snapshots the cumulative counters needed to derive interval rates.
type rawCounters struct {
	pktsIn, pktsOut                uint64
	bytesIn, bytesOut              uint64
	audioIn, videoIn               uint64
	audioOut, videoOut             uint64
	dropped                        uint64
	nacks, plis, retransmits       uint64
	migrations, rebinds            uint64
	plisThrottled, hellosThrottled uint64
}

// RoomStat is one room's routed volume, for the top-rooms view.
type RoomStat struct {
	Room  string
	Bytes uint64
	Pkts  uint64
}

// Snapshot is a point-in-time view of the server for terminal logging. Gauge
// fields (Sessions, Rooms) are current; the *PerSec, Mbps*, Drop*, and health
// delta fields cover the interval since the previous Snapshot call. RTT
// percentiles are cumulative (they mirror the Prometheus histogram).
type Snapshot struct {
	Interval time.Duration
	Sessions int32
	Rooms    int32

	PktsInPerSec   float64
	PktsOutPerSec  float64
	MbpsIn         float64
	MbpsOut        float64
	AudioInPerSec  float64
	VideoInPerSec  float64
	AudioOutPerSec float64
	VideoOutPerSec float64
	DropsPerSec    float64
	DropRatio      float64

	Nacks           uint64
	Plis            uint64
	Retransmits     uint64
	Migrations      uint64
	Rebinds         uint64
	PlisThrottled   uint64
	HellosThrottled uint64

	RTTp50   float64
	RTTp95   float64
	RTTp99   float64
	RTTCount int64

	TotalBytesOut uint64
	TopRooms      []RoomStat
}

// HasEvents reports whether any interval health/reliability counter fired, so
// the caller can omit the noisy reliability line when nothing happened.
func (s Snapshot) HasEvents() bool {
	return s.Nacks|s.Plis|s.Retransmits|s.Migrations|s.Rebinds|s.PlisThrottled|s.HellosThrottled != 0
}

// Snapshot reads the current counters, derives interval rates against the
// previous call, and returns a terminal-friendly view with the top-n rooms.
// The first call establishes a baseline and returns zero rates.
func (m *Metrics) Snapshot(now time.Time, topN int) Snapshot {
	cur := rawCounters{
		pktsIn:          m.PacketsReceived.Load(),
		pktsOut:         m.PacketsSent.Load(),
		bytesIn:         m.BytesReceived.Load(),
		bytesOut:        m.BytesSent.Load(),
		audioIn:         m.AudioPacketsIn.Load(),
		videoIn:         m.VideoPacketsIn.Load(),
		audioOut:        m.AudioPacketsOut.Load(),
		videoOut:        m.VideoPacketsOut.Load(),
		dropped:         m.PacketsDropped.Load() + m.AudioDropped.Load() + m.VideoDropped.Load(),
		nacks:           m.NacksReceived.Load(),
		plis:            m.PlisReceived.Load(),
		retransmits:     m.RetransmitsSent.Load(),
		migrations:      m.Migrations.Load(),
		rebinds:         m.Rebinds.Load(),
		plisThrottled:   m.PlisThrottled.Load(),
		hellosThrottled: m.HellosThrottled.Load(),
	}

	m.snapMu.Lock()
	prev, prevTime, hadPrev := m.prevSnap, m.prevSnapTime, m.snapInited
	m.prevSnap, m.prevSnapTime, m.snapInited = cur, now, true
	m.snapMu.Unlock()

	s := Snapshot{
		Sessions:      m.ActiveSessions.Load(),
		Rooms:         m.ActiveRooms.Load(),
		TotalBytesOut: cur.bytesOut,
		TopRooms:      m.topRooms(topN),
	}
	s.RTTp50, s.RTTp95, s.RTTp99, s.RTTCount = m.rttHist.snapshot()

	if hadPrev {
		if dt := now.Sub(prevTime).Seconds(); dt > 0 {
			s.Interval = now.Sub(prevTime)
			rate := func(c, o uint64) float64 {
				if c < o {
					return 0
				}
				return float64(c-o) / dt
			}
			du := func(c, o uint64) uint64 {
				if c < o {
					return 0
				}
				return c - o
			}
			s.PktsInPerSec = rate(cur.pktsIn, prev.pktsIn)
			s.PktsOutPerSec = rate(cur.pktsOut, prev.pktsOut)
			s.MbpsIn = rate(cur.bytesIn, prev.bytesIn) * 8 / 1e6
			s.MbpsOut = rate(cur.bytesOut, prev.bytesOut) * 8 / 1e6
			s.AudioInPerSec = rate(cur.audioIn, prev.audioIn)
			s.VideoInPerSec = rate(cur.videoIn, prev.videoIn)
			s.AudioOutPerSec = rate(cur.audioOut, prev.audioOut)
			s.VideoOutPerSec = rate(cur.videoOut, prev.videoOut)
			s.DropsPerSec = rate(cur.dropped, prev.dropped)
			if din := du(cur.pktsIn, prev.pktsIn); din > 0 {
				s.DropRatio = float64(du(cur.dropped, prev.dropped)) / float64(din)
			}
			s.Nacks = du(cur.nacks, prev.nacks)
			s.Plis = du(cur.plis, prev.plis)
			s.Retransmits = du(cur.retransmits, prev.retransmits)
			s.Migrations = du(cur.migrations, prev.migrations)
			s.Rebinds = du(cur.rebinds, prev.rebinds)
			s.PlisThrottled = du(cur.plisThrottled, prev.plisThrottled)
			s.HellosThrottled = du(cur.hellosThrottled, prev.hellosThrottled)
		}
	}
	return s
}

// ANSI SGR escape codes used by heat to color terminal health signals.
const (
	ansiReset  = "\x1b[0m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
)

// heat colors text green/yellow/red by threshold (val>=crit red, >=warn yellow),
// or returns it unchanged when color is off. Restrained: only health signals
// get color so the eye lands on trouble, not on every field.
func heat(color bool, text string, val, warn, crit float64) string {
	if !color {
		return text
	}
	c := ansiGreen
	switch {
	case val >= crit:
		c = ansiRed
	case val >= warn:
		c = ansiYellow
	}
	return c + text + ansiReset
}

// Summary renders the compact one-line terminal view. cpuFrac is 0..1 process
// CPU utilization (pass a negative value to omit it). When color is true, the
// three health signals (CPU, drop%, RTT p95) are threshold-colored.
func (s Snapshot) Summary(cpuFrac float64, color bool) string {
	cpu := "cpu=n/a "
	if cpuFrac >= 0 {
		cpu = "cpu=" + heat(color, fmt.Sprintf("%.0f%%", cpuFrac*100), cpuFrac*100, 70, 85) + " "
	}
	dropPct := s.DropRatio * 100
	drop := heat(color, fmt.Sprintf("%.2f%%", dropPct), dropPct, 0.5, 2)
	p95 := heat(color, fmt.Sprintf("%.0f", s.RTTp95), s.RTTp95, 150, 300)
	return fmt.Sprintf(
		"sess=%d rooms=%d %s│ in %s pps %.2f Mbps │ out %s pps %.2f Mbps │ drop %s %s/s │ rtt p50<=%.0f p95<=%s p99<=%.0f ms (n=%d)",
		s.Sessions, s.Rooms, cpu,
		humanRate(s.PktsInPerSec), s.MbpsIn,
		humanRate(s.PktsOutPerSec), s.MbpsOut,
		drop, humanRate(s.DropsPerSec),
		s.RTTp50, p95, s.RTTp99, s.RTTCount,
	)
}

// Reliability renders the interval health line (NACK/PLI/retransmit/migration
// churn). Empty when nothing fired; guard with HasEvents.
func (s Snapshot) Reliability() string {
	return fmt.Sprintf(
		"nack=%d pli=%d rtx=%d migrate=%d rebind=%d pli_throttled=%d hello_throttled=%d",
		s.Nacks, s.Plis, s.Retransmits, s.Migrations, s.Rebinds, s.PlisThrottled, s.HellosThrottled,
	)
}

// humanRate formats a per-second rate compactly, switching to a "k" suffix at
// >=1000 (two decimals) and >=10000 (one decimal) so wide columns stay narrow.
func humanRate(v float64) string {
	switch {
	case v >= 10000:
		return fmt.Sprintf("%.1fk", v/1000)
	case v >= 1000:
		return fmt.Sprintf("%.2fk", v/1000)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// topRooms returns the n rooms with the most routed bytes, sorted descending.
func (m *Metrics) topRooms(n int) []RoomStat {
	var rooms []RoomStat
	m.roomStats.Range(func(k, v any) bool {
		rm := v.(*roomMetrics)
		rooms = append(rooms, RoomStat{Room: k.(string), Bytes: rm.BytesRouted.Load(), Pkts: rm.PacketsRouted.Load()})
		return true
	})
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Bytes > rooms[j].Bytes })
	if n > 0 && len(rooms) > n {
		rooms = rooms[:n]
	}
	return rooms
}

// snapshot estimates p50/p95/p99 latency (as bucket upper bounds) and the total
// count from the cumulative histogram.
func (h *histogram) snapshot() (p50, p95, p99 float64, count int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	count = h.count
	if count == 0 {
		return 0, 0, 0, 0
	}
	q := func(p float64) float64 {
		target := p * float64(count)
		cum := int64(0)
		for i, b := range h.buckets {
			cum += b
			if float64(cum) >= target {
				if i < len(h.bounds) {
					return h.bounds[i]
				}
				break
			}
		}
		return h.bounds[len(h.bounds)-1]
	}
	return q(0.50), q(0.95), q(0.99), count
}
