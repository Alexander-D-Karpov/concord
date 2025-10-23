package telemetry

import (
	"context"
	"fmt"
	"net/http"
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

	customMetrics map[string]interface{}
}

func NewMetrics(logger *zap.Logger) *Metrics {
	return &Metrics{
		logger:        logger,
		customMetrics: make(map[string]interface{}),
	}
}

func (m *Metrics) Start(ctx context.Context, port int, path string) error {
	mux := http.NewServeMux()
	mux.HandleFunc(path, m.handleMetrics)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	m.logger.Info("metrics server starting",
		zap.Int("port", port),
		zap.String("path", path),
	)

	errChan := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return server.Shutdown(context.Background())
	}
}

func (m *Metrics) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")

	write := func(format string, args ...interface{}) bool {
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			m.logger.Error("failed to write metrics response", zap.Error(err))
			return false
		}
		return true
	}

	if !write("# HELP voice_packets_received_total Total packets received\n") {
		return
	}
	if !write("# TYPE voice_packets_received_total counter\n") {
		return
	}
	if !write("voice_packets_received_total %d\n", m.PacketsReceived.Load()) {
		return
	}

	if !write("# HELP voice_packets_sent_total Total packets sent\n") {
		return
	}
	if !write("# TYPE voice_packets_sent_total counter\n") {
		return
	}
	if !write("voice_packets_sent_total %d\n", m.PacketsSent.Load()) {
		return
	}

	if !write("# HELP voice_bytes_received_total Total bytes received\n") {
		return
	}
	if !write("# TYPE voice_bytes_received_total counter\n") {
		return
	}
	if !write("voice_bytes_received_total %d\n", m.BytesReceived.Load()) {
		return
	}

	if !write("# HELP voice_bytes_sent_total Total bytes sent\n") {
		return
	}
	if !write("# TYPE voice_bytes_sent_total counter\n") {
		return
	}
	if !write("voice_bytes_sent_total %d\n", m.BytesSent.Load()) {
		return
	}

	if !write("# HELP voice_active_sessions Current active sessions\n") {
		return
	}
	if !write("# TYPE voice_active_sessions gauge\n") {
		return
	}
	if !write("voice_active_sessions %d\n", m.ActiveSessions.Load()) {
		return
	}

	if !write("# HELP voice_active_rooms Current active rooms\n") {
		return
	}
	if !write("# TYPE voice_active_rooms gauge\n") {
		return
	}
	if !write("voice_active_rooms %d\n", m.ActiveRooms.Load()) {
		return
	}

	if !write("# HELP voice_packets_dropped_total Total packets dropped\n") {
		return
	}
	if !write("# TYPE voice_packets_dropped_total counter\n") {
		return
	}
	if !write("voice_packets_dropped_total %d\n", m.PacketsDropped.Load()) {
		return
	}
}

func (m *Metrics) RecordPacketReceived(bytes uint64) {
	m.PacketsReceived.Add(1)
	m.BytesReceived.Add(bytes)
}

func (m *Metrics) RecordPacketSent(bytes uint64) {
	m.PacketsSent.Add(1)
	m.BytesSent.Add(bytes)
}

func (m *Metrics) RecordPacketDropped() {
	m.PacketsDropped.Add(1)
}

func (m *Metrics) SetActiveSessions(count int32) {
	m.ActiveSessions.Store(count)
}

func (m *Metrics) SetActiveRooms(count int32) {
	m.ActiveRooms.Store(count)
}

func (m *Metrics) GetStats() Stats {
	return Stats{
		PacketsReceived: m.PacketsReceived.Load(),
		PacketsSent:     m.PacketsSent.Load(),
		BytesReceived:   m.BytesReceived.Load(),
		BytesSent:       m.BytesSent.Load(),
		ActiveSessions:  m.ActiveSessions.Load(),
		ActiveRooms:     m.ActiveRooms.Load(),
		PacketsDropped:  m.PacketsDropped.Load(),
	}
}

type Stats struct {
	PacketsReceived uint64
	PacketsSent     uint64
	BytesReceived   uint64
	BytesSent       uint64
	ActiveSessions  int32
	ActiveRooms     int32
	PacketsDropped  uint64
}
