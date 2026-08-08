package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Metrics is the voice server's counter set, exported over HTTP as Prometheus
// text and JSON. All counters are lock-free atomics updated on hot paths; the
// exported fields may be read directly. roomStats, rttHist, and the snap* fields
// carry their own synchronization. A zero Metrics is not usable — construct with
// NewMetrics so the RTT histogram is allocated.
type Metrics struct {
	logger *zap.Logger

	PacketsReceived atomic.Uint64
	PacketsSent     atomic.Uint64
	BytesReceived   atomic.Uint64
	BytesSent       atomic.Uint64
	ActiveSessions  atomic.Int32
	ActiveRooms     atomic.Int32
	PacketsDropped  atomic.Uint64
	ControlDropped  atomic.Uint64
	ControlSent     atomic.Uint64

	AudioPacketsIn    atomic.Uint64
	VideoPacketsIn    atomic.Uint64
	AudioPacketsOut   atomic.Uint64
	VideoPacketsOut   atomic.Uint64
	NacksReceived     atomic.Uint64
	PlisReceived      atomic.Uint64
	RetransmitsSent   atomic.Uint64
	HellosReceived    atomic.Uint64
	WelcomesSent      atomic.Uint64
	ByesReceived      atomic.Uint64
	PingsReceived     atomic.Uint64
	PongsSent         atomic.Uint64
	SubscriptionsRx   atomic.Uint64
	QualityReportsRx  atomic.Uint64
	ReceiverReportsRx atomic.Uint64

	Migrations        atomic.Uint64
	Rebinds           atomic.Uint64
	PlisThrottled     atomic.Uint64
	HellosThrottled   atomic.Uint64
	AudioDropped      atomic.Uint64
	VideoDropped      atomic.Uint64
	VideoNoSubscriber atomic.Uint64

	roomStats sync.Map
	rttHist   *histogram

	snapMu       sync.Mutex
	prevSnap     rawCounters
	prevSnapTime time.Time
	snapInited   bool
}

// roomMetrics accumulates per-room routed volume; stored by value's pointer in
// Metrics.roomStats and updated atomically without holding the map lock.
type roomMetrics struct {
	PacketsRouted atomic.Uint64
	BytesRouted   atomic.Uint64
}

// histogram is a fixed-bucket cumulative latency histogram guarded by its own
// mutex. bounds are the inclusive upper edges (ms); buckets has one extra slot
// for the +Inf overflow. count and sum feed Prometheus _count/_sum.
type histogram struct {
	mu      sync.Mutex
	buckets []int64
	bounds  []float64
	count   int64
	sum     float64
}

// Stats is a plain-value snapshot of the core counters for the JSON endpoint
// and the status API. It is a subset of Metrics (the video/throttle/migration
// counters are Prometheus-only) taken via GetStats.
type Stats struct {
	PacketsReceived   uint64 `json:"packets_received"`
	PacketsSent       uint64 `json:"packets_sent"`
	BytesReceived     uint64 `json:"bytes_received"`
	BytesSent         uint64 `json:"bytes_sent"`
	ActiveSessions    int32  `json:"active_sessions"`
	ActiveRooms       int32  `json:"active_rooms"`
	PacketsDropped    uint64 `json:"packets_dropped"`
	ControlDropped    uint64 `json:"control_dropped"`
	ControlSent       uint64 `json:"control_sent"`
	AudioPacketsIn    uint64 `json:"audio_packets_in"`
	VideoPacketsIn    uint64 `json:"video_packets_in"`
	AudioPacketsOut   uint64 `json:"audio_packets_out"`
	VideoPacketsOut   uint64 `json:"video_packets_out"`
	NacksReceived     uint64 `json:"nacks_received"`
	PlisReceived      uint64 `json:"plis_received"`
	RetransmitsSent   uint64 `json:"retransmits_sent"`
	HellosReceived    uint64 `json:"hellos_received"`
	WelcomesSent      uint64 `json:"welcomes_sent"`
	ByesReceived      uint64 `json:"byes_received"`
	PingsReceived     uint64 `json:"pings_received"`
	PongsSent         uint64 `json:"pongs_sent"`
	SubscriptionsRx   uint64 `json:"subscriptions_received"`
	QualityReportsRx  uint64 `json:"quality_reports_received"`
	ReceiverReportsRx uint64 `json:"receiver_reports_received"`
}

// NewMetrics returns a Metrics with the RTT histogram bucketed at
// 1..1000 ms. All counters start at zero.
func NewMetrics(logger *zap.Logger) *Metrics {
	return &Metrics{
		logger: logger,
		rttHist: &histogram{
			bounds:  []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000},
			buckets: make([]int64, 10),
		},
	}
}

// Start serves Prometheus text at path and JSON at /metrics/json on the given
// port, blocking until ctx is cancelled (then it drains via Shutdown) or
// ListenAndServe fails. Returns the listen/serve error, or nil on clean
// context-cancelled shutdown.
func (m *Metrics) Start(ctx context.Context, port int, path string) error {
	mux := http.NewServeMux()
	mux.HandleFunc(path, m.handleProm)
	mux.HandleFunc("/metrics/json", m.handleJSON)
	server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}
	m.logger.Info("metrics server starting", zap.Int("port", port))

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return server.Shutdown(context.Background())
	}
}

// handleProm writes the full counter set in Prometheus text exposition format,
// including the derived voice_media_drop_ratio gauge, the RTT histogram, and the
// top-20 per-room gauges. Counter snapshots are read independently, so the
// output is not a single consistent instant.
func (m *Metrics) handleProm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	p := func(name, help, typ string, val uint64) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, typ, name, val)
	}
	g := func(name, help string, val int32) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, val)
	}
	p("voice_packets_received_total", "Total packets received", "counter", m.PacketsReceived.Load())
	p("voice_packets_sent_total", "Total packets sent", "counter", m.PacketsSent.Load())
	p("voice_bytes_received_total", "Bytes received", "counter", m.BytesReceived.Load())
	p("voice_bytes_sent_total", "Bytes sent", "counter", m.BytesSent.Load())
	g("voice_active_sessions", "Active sessions", m.ActiveSessions.Load())
	g("voice_active_rooms", "Active rooms", m.ActiveRooms.Load())
	p("voice_packets_dropped_total", "Dropped media packets", "counter", m.PacketsDropped.Load())
	p("voice_control_dropped_total", "Dropped control packets", "counter", m.ControlDropped.Load())
	p("voice_control_sent_total", "Sent control packets", "counter", m.ControlSent.Load())
	p("voice_audio_in_total", "Audio packets in", "counter", m.AudioPacketsIn.Load())
	p("voice_video_in_total", "Video packets in", "counter", m.VideoPacketsIn.Load())
	p("voice_audio_out_total", "Audio packets out", "counter", m.AudioPacketsOut.Load())
	p("voice_video_out_total", "Video packets out", "counter", m.VideoPacketsOut.Load())
	p("voice_nacks_total", "NACKs received", "counter", m.NacksReceived.Load())
	p("voice_plis_total", "PLIs received", "counter", m.PlisReceived.Load())
	p("voice_retransmits_total", "Retransmits", "counter", m.RetransmitsSent.Load())
	p("voice_hellos_total", "Hellos", "counter", m.HellosReceived.Load())
	p("voice_welcomes_total", "Welcomes", "counter", m.WelcomesSent.Load())
	p("voice_byes_total", "Byes", "counter", m.ByesReceived.Load())
	p("voice_pings_total", "Pings received", "counter", m.PingsReceived.Load())
	p("voice_pongs_total", "Pongs sent", "counter", m.PongsSent.Load())
	p("voice_subscriptions_total", "Subscriptions received", "counter", m.SubscriptionsRx.Load())
	p("voice_quality_reports_total", "Quality reports received", "counter", m.QualityReportsRx.Load())
	p("voice_receiver_reports_total", "Receiver reports received", "counter", m.ReceiverReportsRx.Load())
	p("voice_migrations_total", "Session address migrations", "counter", m.Migrations.Load())
	p("voice_rebinds_total", "Session rebinds via hello", "counter", m.Rebinds.Load())
	p("voice_plis_throttled_total", "PLIs suppressed by throttle", "counter", m.PlisThrottled.Load())
	p("voice_hellos_throttled_total", "Hellos shed by flood protection", "counter", m.HellosThrottled.Load())
	p("voice_audio_dropped_total", "Audio packets dropped (audio queue full)", "counter", m.AudioDropped.Load())
	p("voice_video_dropped_total", "Video packets dropped (video queue full)", "counter", m.VideoDropped.Load())
	p("voice_video_no_subscriber_total", "Video packets received that reached no receiver", "counter", m.VideoNoSubscriber.Load())

	recv := m.PacketsReceived.Load()
	drop := m.PacketsDropped.Load() + m.AudioDropped.Load() + m.VideoDropped.Load()
	ratio := 0.0
	if recv > 0 {
		ratio = float64(drop) / float64(recv)
	}
	_, _ = fmt.Fprintf(w, "# HELP voice_media_drop_ratio Fraction of media packets dropped\n# TYPE voice_media_drop_ratio gauge\nvoice_media_drop_ratio %f\n", ratio)

	m.rttHist.mu.Lock()
	cum := int64(0)
	for i, b := range m.rttHist.buckets {
		cum += b
		if i < len(m.rttHist.bounds) {
			_, _ = fmt.Fprintf(w, "voice_rtt_ms_bucket{le=\"%.0f\"} %d\n", m.rttHist.bounds[i], cum)
		} else {
			_, _ = fmt.Fprintf(w, "voice_rtt_ms_bucket{le=\"+Inf\"} %d\n", cum)
		}
	}
	_, _ = fmt.Fprintf(w, "voice_rtt_ms_sum %f\nvoice_rtt_ms_count %d\n", m.rttHist.sum, m.rttHist.count)
	m.rttHist.mu.Unlock()

	m.writeTopRooms(w, 20)
}

// writeTopRooms exports the top-N rooms by routed bytes (bounded label
// cardinality) — surfacing the previously-collected-but-unexported roomStats.
func (m *Metrics) writeTopRooms(w http.ResponseWriter, n int) {
	rooms := m.topRooms(n)
	if len(rooms) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "# HELP voice_room_routed_bytes Bytes routed per room (top %d)\n# TYPE voice_room_routed_bytes gauge\n", n)
	for _, r := range rooms {
		_, _ = fmt.Fprintf(w, "voice_room_routed_bytes{room=%q} %d\n", r.Room, r.Bytes)
	}
	_, _ = fmt.Fprintf(w, "# HELP voice_room_routed_packets Packets routed per room (top %d)\n# TYPE voice_room_routed_packets gauge\n", n)
	for _, r := range rooms {
		_, _ = fmt.Fprintf(w, "voice_room_routed_packets{room=%q} %d\n", r.Room, r.Pkts)
	}
}

// handleJSON writes GetStats as JSON — the human/dashboard-friendly subset of
// the Prometheus output.
func (m *Metrics) handleJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m.GetStats())
}

// RecordPacketReceived counts one inbound packet and adds its wire size (bytes)
// to BytesReceived. Concurrency-safe; called on every UDP read.
func (m *Metrics) RecordPacketReceived(bytes uint64) {
	m.PacketsReceived.Add(1)
	m.BytesReceived.Add(bytes)
}

// RecordPacketSent counts one outbound packet and adds bytes to BytesSent.
func (m *Metrics) RecordPacketSent(bytes uint64) { m.PacketsSent.Add(1); m.BytesSent.Add(bytes) }

// RecordPacketDropped counts a media packet dropped for reasons other than a
// full audio/video queue (those use RecordAudioDropped/RecordVideoDropped).
func (m *Metrics) RecordPacketDropped() { m.PacketsDropped.Add(1) }

// RecordControlDropped counts a control-plane packet that could not be sent
// (e.g. control send queue full).
func (m *Metrics) RecordControlDropped() { m.ControlDropped.Add(1) }

// RecordControlSent counts one successfully emitted control-plane packet.
func (m *Metrics) RecordControlSent() { m.ControlSent.Add(1) }

// RecordAudioIn counts one received audio media packet.
func (m *Metrics) RecordAudioIn() { m.AudioPacketsIn.Add(1) }

// RecordVideoIn counts one received video media packet.
func (m *Metrics) RecordVideoIn() { m.VideoPacketsIn.Add(1) }

// RecordAudioOut counts one forwarded audio packet (one destination).
func (m *Metrics) RecordAudioOut() { m.AudioPacketsOut.Add(1) }

// RecordVideoOut counts one forwarded video packet (one destination).
func (m *Metrics) RecordVideoOut() { m.VideoPacketsOut.Add(1) }

// RecordAudioOutN adds n forwarded audio packets at once — one fan-out to n
// subscribers counted in a single atomic add.
func (m *Metrics) RecordAudioOutN(n uint64) { m.AudioPacketsOut.Add(n) }

// RecordVideoOutN adds n forwarded video packets at once (fan-out to n subscribers).
func (m *Metrics) RecordVideoOutN(n uint64) { m.VideoPacketsOut.Add(n) }

// RecordNack counts one received NACK (RTCP retransmission request).
func (m *Metrics) RecordNack() { m.NacksReceived.Add(1) }

// RecordPli counts one received PLI (picture-loss indication / keyframe request).
func (m *Metrics) RecordPli() { m.PlisReceived.Add(1) }

// RecordRetransmit counts one packet resent in response to a NACK.
func (m *Metrics) RecordRetransmit() { m.RetransmitsSent.Add(1) }

// RecordHello counts one received HELLO (session handshake/bind).
func (m *Metrics) RecordHello() { m.HellosReceived.Add(1) }

// RecordWelcome counts one WELCOME sent in reply to a HELLO.
func (m *Metrics) RecordWelcome() { m.WelcomesSent.Add(1) }

// RecordBye counts one received BYE (client-signalled disconnect).
func (m *Metrics) RecordBye() { m.ByesReceived.Add(1) }

// RecordPing counts one received keepalive PING.
func (m *Metrics) RecordPing() { m.PingsReceived.Add(1) }

// RecordPong counts one PONG sent in reply to a PING.
func (m *Metrics) RecordPong() { m.PongsSent.Add(1) }

// RecordSubscribe counts one received subscription request.
func (m *Metrics) RecordSubscribe() { m.SubscriptionsRx.Add(1) }

// RecordQualityReport counts one received client quality report.
func (m *Metrics) RecordQualityReport() { m.QualityReportsRx.Add(1) }

// RecordReceiverReport counts one received RTCP receiver report.
func (m *Metrics) RecordReceiverReport() { m.ReceiverReportsRx.Add(1) }

// RecordMigration counts one session whose peer address changed (e.g. NAT rebind).
func (m *Metrics) RecordMigration() { m.Migrations.Add(1) }

// RecordRebind counts one session re-established via a fresh HELLO on an existing SSRC.
func (m *Metrics) RecordRebind() { m.Rebinds.Add(1) }

// RecordPliThrottled counts one PLI suppressed by the rate limiter rather than forwarded.
func (m *Metrics) RecordPliThrottled() { m.PlisThrottled.Add(1) }

// RecordHelloThrottled counts one HELLO shed by flood protection.
func (m *Metrics) RecordHelloThrottled() { m.HellosThrottled.Add(1) }

// RecordAudioDropped counts one audio packet dropped because the audio send queue was full.
func (m *Metrics) RecordAudioDropped() { m.AudioDropped.Add(1) }

// RecordVideoDropped counts one video packet dropped because the video send queue was full.
func (m *Metrics) RecordVideoDropped() { m.VideoDropped.Add(1) }

// RecordVideoNoSubscriber counts one received video packet that reached no
// receiver (no subscribers), useful for spotting wasted uplink.
func (m *Metrics) RecordVideoNoSubscriber() { m.VideoNoSubscriber.Add(1) }

// RecordRTT feeds one round-trip sample (milliseconds) into the RTT histogram.
func (m *Metrics) RecordRTT(ms float64) { m.rttHist.observe(ms) }

// SetActiveSessions stores the current live session gauge (absolute value, not a delta).
func (m *Metrics) SetActiveSessions(c int32) { m.ActiveSessions.Store(c) }

// SetActiveRooms stores the current live room gauge (absolute value, not a delta).
func (m *Metrics) SetActiveRooms(c int32) { m.ActiveRooms.Store(c) }

// RecordRoomRouted attributes one routed packet and its bytes to roomID,
// lazily creating the room's counter. Feeds the top-rooms view; concurrency-safe.
func (m *Metrics) RecordRoomRouted(roomID string, bytes uint64) {
	v, _ := m.roomStats.LoadOrStore(roomID, &roomMetrics{})
	rm := v.(*roomMetrics)
	rm.PacketsRouted.Add(1)
	rm.BytesRouted.Add(bytes)
}

// GetStats reads the core counters into a Stats value. Each field is loaded
// independently, so the result is a near-instant, not a locked-consistent, view.
func (m *Metrics) GetStats() Stats {
	return Stats{
		PacketsReceived:   m.PacketsReceived.Load(),
		PacketsSent:       m.PacketsSent.Load(),
		BytesReceived:     m.BytesReceived.Load(),
		BytesSent:         m.BytesSent.Load(),
		ActiveSessions:    m.ActiveSessions.Load(),
		ActiveRooms:       m.ActiveRooms.Load(),
		PacketsDropped:    m.PacketsDropped.Load(),
		ControlDropped:    m.ControlDropped.Load(),
		ControlSent:       m.ControlSent.Load(),
		AudioPacketsIn:    m.AudioPacketsIn.Load(),
		VideoPacketsIn:    m.VideoPacketsIn.Load(),
		AudioPacketsOut:   m.AudioPacketsOut.Load(),
		VideoPacketsOut:   m.VideoPacketsOut.Load(),
		NacksReceived:     m.NacksReceived.Load(),
		PlisReceived:      m.PlisReceived.Load(),
		RetransmitsSent:   m.RetransmitsSent.Load(),
		HellosReceived:    m.HellosReceived.Load(),
		WelcomesSent:      m.WelcomesSent.Load(),
		ByesReceived:      m.ByesReceived.Load(),
		PingsReceived:     m.PingsReceived.Load(),
		PongsSent:         m.PongsSent.Load(),
		SubscriptionsRx:   m.SubscriptionsRx.Load(),
		QualityReportsRx:  m.QualityReportsRx.Load(),
		ReceiverReportsRx: m.ReceiverReportsRx.Load(),
	}
}

// observe records one value into its bucket (values above the top bound land in
// the +Inf overflow slot) and updates count and sum. Holds the histogram lock.
func (h *histogram) observe(val float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	idx := sort.SearchFloat64s(h.bounds, val)
	if idx >= len(h.buckets) {
		idx = len(h.buckets) - 1
	}
	h.buckets[idx]++
	h.count++
	h.sum += val
}
