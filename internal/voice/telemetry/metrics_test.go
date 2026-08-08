package telemetry

import (
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPromExportsNewMetricsAndTopRooms(t *testing.T) {
	m := NewMetrics(zap.NewNop())
	m.RecordMigration()
	m.RecordRebind()
	m.RecordHelloThrottled()
	m.RecordPliThrottled()
	m.RecordRoomRouted("room-a", 5000)
	m.RecordRoomRouted("room-b", 100)

	rec := httptest.NewRecorder()
	m.handleProm(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		"voice_migrations_total 1",
		"voice_rebinds_total 1",
		"voice_hellos_throttled_total 1",
		"voice_plis_throttled_total 1",
		"voice_media_drop_ratio",
		`voice_room_routed_bytes{room="room-a"} 5000`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("prometheus output missing %q\n---\n%s", want, body)
		}
	}
}

// Snapshot must derive per-interval rates (not cumulative totals) from the
// delta between two calls.
func TestSnapshotDerivesIntervalRates(t *testing.T) {
	m := NewMetrics(zap.NewNop())
	t0 := time.Unix(1_700_000_000, 0)
	m.Snapshot(t0, 3) // establish baseline

	// 10s of traffic: 1000 pkts in @200B, 800 out @250B, 10 dropped.
	for i := 0; i < 1000; i++ {
		m.RecordPacketReceived(200)
	}
	for i := 0; i < 800; i++ {
		m.RecordPacketSent(250)
	}
	for i := 0; i < 10; i++ {
		m.RecordPacketDropped()
	}

	s := m.Snapshot(t0.Add(10*time.Second), 3)
	approx := func(name string, got, want float64) {
		if math.Abs(got-want) > 1e-6 {
			t.Fatalf("%s: got %v want %v", name, got, want)
		}
	}
	approx("pps_in", s.PktsInPerSec, 100)                    // 1000/10
	approx("pps_out", s.PktsOutPerSec, 80)                   // 800/10
	approx("mbps_in", s.MbpsIn, float64(1000*200)*8/1e6/10)  // 0.16
	approx("mbps_out", s.MbpsOut, float64(800*250)*8/1e6/10) // 0.16
	approx("drops_per_sec", s.DropsPerSec, 1)                // 10/10
	approx("drop_ratio", s.DropRatio, 0.01)                  // 10/1000
	if !s.HasEvents() {
		// no NACK/PLI/migration fired here
		t.Logf("no reliability events (expected)")
	}
}

// RTT percentiles must reflect the tail: 95% fast, 5% at 500ms.
func TestSnapshotRTTPercentiles(t *testing.T) {
	m := NewMetrics(zap.NewNop())
	for i := 0; i < 95; i++ {
		m.RecordRTT(10)
	}
	for i := 0; i < 5; i++ {
		m.RecordRTT(500)
	}
	s := m.Snapshot(time.Unix(1_700_000_000, 0), 3)
	if s.RTTCount != 100 {
		t.Fatalf("rtt count: got %d want 100", s.RTTCount)
	}
	if s.RTTp50 > 10 {
		t.Fatalf("p50 should be <=10ms, got %v", s.RTTp50)
	}
	if s.RTTp99 < 100 {
		t.Fatalf("p99 should reflect the 500ms tail, got %v", s.RTTp99)
	}
}

// The summary must be plain when color is off, and must colorize a bad drop rate
// red when color is on.
func TestSummaryColoring(t *testing.T) {
	s := Snapshot{Sessions: 3, DropRatio: 0.05} // 5% drop -> critical

	plain := s.Summary(0.2, false)
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("color-off summary must contain no ANSI codes: %q", plain)
	}

	colored := s.Summary(0.2, true)
	if !strings.Contains(colored, ansiRed+"5.00%"+ansiReset) {
		t.Fatalf("critical drop rate should be red, got: %q", colored)
	}
}
