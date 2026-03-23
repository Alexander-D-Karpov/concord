package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
)

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

	roomStats sync.Map
	rttHist   *histogram
}

type roomMetrics struct {
	PacketsRouted atomic.Uint64
	BytesRouted   atomic.Uint64
}

type histogram struct {
	mu      sync.Mutex
	buckets []int64
	bounds  []float64
	count   int64
	sum     float64
}

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

func NewMetrics(logger *zap.Logger) *Metrics {
	return &Metrics{
		logger: logger,
		rttHist: &histogram{
			bounds:  []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000},
			buckets: make([]int64, 10),
		},
	}
}

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
}

func (m *Metrics) handleJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m.GetStats())
}

func (m *Metrics) RecordPacketReceived(bytes uint64) {
	m.PacketsReceived.Add(1)
	m.BytesReceived.Add(bytes)
}
func (m *Metrics) RecordPacketSent(bytes uint64) { m.PacketsSent.Add(1); m.BytesSent.Add(bytes) }
func (m *Metrics) RecordPacketDropped()          { m.PacketsDropped.Add(1) }
func (m *Metrics) RecordControlDropped()         { m.ControlDropped.Add(1) }
func (m *Metrics) RecordControlSent()            { m.ControlSent.Add(1) }
func (m *Metrics) RecordAudioIn()                { m.AudioPacketsIn.Add(1) }
func (m *Metrics) RecordVideoIn()                { m.VideoPacketsIn.Add(1) }
func (m *Metrics) RecordAudioOut()               { m.AudioPacketsOut.Add(1) }
func (m *Metrics) RecordVideoOut()               { m.VideoPacketsOut.Add(1) }
func (m *Metrics) RecordAudioOutN(n uint64)      { m.AudioPacketsOut.Add(n) }
func (m *Metrics) RecordVideoOutN(n uint64)      { m.VideoPacketsOut.Add(n) }
func (m *Metrics) RecordNack()                   { m.NacksReceived.Add(1) }
func (m *Metrics) RecordPli()                    { m.PlisReceived.Add(1) }
func (m *Metrics) RecordRetransmit()             { m.RetransmitsSent.Add(1) }
func (m *Metrics) RecordHello()                  { m.HellosReceived.Add(1) }
func (m *Metrics) RecordWelcome()                { m.WelcomesSent.Add(1) }
func (m *Metrics) RecordBye()                    { m.ByesReceived.Add(1) }
func (m *Metrics) RecordPing()                   { m.PingsReceived.Add(1) }
func (m *Metrics) RecordPong()                   { m.PongsSent.Add(1) }
func (m *Metrics) RecordSubscribe()              { m.SubscriptionsRx.Add(1) }
func (m *Metrics) RecordQualityReport()          { m.QualityReportsRx.Add(1) }
func (m *Metrics) RecordReceiverReport()         { m.ReceiverReportsRx.Add(1) }
func (m *Metrics) RecordRTT(ms float64)          { m.rttHist.observe(ms) }
func (m *Metrics) SetActiveSessions(c int32)     { m.ActiveSessions.Store(c) }
func (m *Metrics) SetActiveRooms(c int32)        { m.ActiveRooms.Store(c) }

func (m *Metrics) RecordRoomRouted(roomID string, bytes uint64) {
	v, _ := m.roomStats.LoadOrStore(roomID, &roomMetrics{})
	rm := v.(*roomMetrics)
	rm.PacketsRouted.Add(1)
	rm.BytesRouted.Add(bytes)
}

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
