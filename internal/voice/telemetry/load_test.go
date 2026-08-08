package telemetry

import (
	"testing"
	"time"
)

func TestLoadSamplerEgressRate(t *testing.T) {
	l := NewLoadSampler()
	t0 := time.Unix(1_700_000_000, 0)

	if cpu, mbps := l.Sample(0, t0); cpu != 0 || mbps != 0 {
		t.Fatalf("first sample must be baseline zeros: cpu=%v mbps=%v", cpu, mbps)
	}

	// 1,000,000 bytes over 1s = 8 Mbps
	_, mbps := l.Sample(1_000_000, t0.Add(time.Second))
	if mbps < 7.9 || mbps > 8.1 {
		t.Fatalf("expected ~8 Mbps, got %v", mbps)
	}

	// cumulative counter must not go negative on a reset
	if _, mbps := l.Sample(0, t0.Add(2*time.Second)); mbps != 0 {
		t.Fatalf("counter reset should yield 0 Mbps, got %v", mbps)
	}
}
