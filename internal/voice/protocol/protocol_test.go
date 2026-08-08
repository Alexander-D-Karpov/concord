package protocol

import (
	"encoding/json"
	"testing"
)

func TestHelloCapabilitiesRoundTrip(t *testing.T) {
	in := HelloPayload{
		Token:        "t",
		Protocol:     3,
		Codec:        "opus",
		Capabilities: &Capabilities{FEC: true, DTX: true, MaxBitrate: 1_500_000},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out HelloPayload
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Capabilities == nil || !out.Capabilities.FEC || !out.Capabilities.DTX || out.Capabilities.MaxBitrate != 1_500_000 {
		t.Fatalf("capabilities not preserved: %+v", out.Capabilities)
	}
}

func TestParticipantInfoCapabilitiesOmitEmpty(t *testing.T) {
	b, err := json.Marshal(ParticipantInfo{UserID: "u"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["max_bitrate"]; ok {
		t.Fatal("max_bitrate should be omitted when zero")
	}
}

func TestBitrateHintRoundTrip(t *testing.T) {
	pkt := BuildBitrateHint(0xDEADBEEF, 1_200_000, 1)
	if len(pkt) != 10 {
		t.Fatalf("want 10 bytes, got %d", len(pkt))
	}
	if pkt[0] != PacketTypeBitrateHint {
		t.Fatalf("wrong type byte: 0x%02x", pkt[0])
	}
	p, err := ParseBitrateHint(pkt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.SSRC != 0xDEADBEEF || p.TargetBps != 1_200_000 || p.Reason != 1 {
		t.Fatalf("round-trip mismatch: %+v", p)
	}
	if _, err := ParseBitrateHint([]byte{PacketTypeBitrateHint, 0x00}); err == nil {
		t.Fatal("expected error on short buffer")
	}
}

func TestProtocolVersionIs3(t *testing.T) {
	if ProtocolVersion != 3 {
		t.Fatalf("ProtocolVersion should be 3, got %d", ProtocolVersion)
	}
}

// BuildReceiverReport is the inverse of ParseReceiverReport: a report built by a
// client must parse back on the server to the same values (fraction_lost is 8-bit
// quantized, total_lost is 24-bit).
func TestBuildReceiverReportRoundTrip(t *testing.T) {
	in := ReceiverReport{
		SSRC:         1111,
		ReporterSSRC: 2222,
		FractionLost: 0.2,
		TotalLost:    100000,
		HighestSeq:   65000,
		Jitter:       480,
		LastSR:       0xDEADBEEF,
	}

	buf := BuildReceiverReport(in)
	if len(buf) != 25 {
		t.Fatalf("want 25-byte RR, got %d", len(buf))
	}
	if buf[0] != PacketTypeRR {
		t.Fatalf("want type RR %#x, got %#x", PacketTypeRR, buf[0])
	}

	out, err := ParseReceiverReport(buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.SSRC != in.SSRC || out.ReporterSSRC != in.ReporterSSRC {
		t.Fatalf("ssrc mismatch: %+v", out)
	}
	if out.TotalLost != in.TotalLost || out.HighestSeq != in.HighestSeq ||
		out.Jitter != in.Jitter || out.LastSR != in.LastSR {
		t.Fatalf("field mismatch: got %+v want %+v", out, in)
	}
	wantFrac := float64(uint8(in.FractionLost*255)) / 255.0
	if d := out.FractionLost - wantFrac; d > 1e-9 || d < -1e-9 {
		t.Fatalf("fraction_lost mismatch: got %v want %v", out.FractionLost, wantFrac)
	}
}
