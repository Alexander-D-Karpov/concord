package main

import (
	"bytes"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
)

// A tone frame must be exactly one 20ms s16le frame and carry real signal energy,
// and successive frames must differ (the tone is moving, so the VU visibly reacts).
func TestToneFrameSizeAndSignal(t *testing.T) {
	src := &toneSource{}
	f0 := src.frame(0)
	if len(f0) != audioFrameSamples*2 {
		t.Fatalf("tone frame size = %d, want %d", len(f0), audioFrameSamples*2)
	}
	if rms := pcmRMS(f0); rms < 0.05 {
		t.Fatalf("tone RMS = %.3f, want > 0.05 (real signal)", rms)
	}
	f1 := src.frame(1)
	if bytes.Equal(f0, f1) {
		t.Fatal("successive tone frames must differ")
	}
}

// The palette video codec must round-trip a frame exactly: encode then decode
// recovers identical dimensions and every pixel index.
func TestVideoEncodeDecodeRoundTrip(t *testing.T) {
	src := &videoSource{}
	f := src.frame(7)
	if f.w != videoW || f.h != videoH || len(f.px) != videoW*videoH {
		t.Fatalf("frame shape wrong: %dx%d px=%d", f.w, f.h, len(f.px))
	}
	enc := encodeFrame(f)
	if len(enc) > protocol.MaxUDPPayload {
		t.Fatalf("encoded frame %d bytes exceeds MaxUDPPayload %d", len(enc), protocol.MaxUDPPayload)
	}
	dec, err := decodeFrame(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.w != f.w || dec.h != f.h || !bytes.Equal(dec.px, f.px) {
		t.Fatal("frame did not round-trip through encode/decode")
	}
}

// The animation must actually move — two well-separated frames differ.
func TestVideoFrameAnimates(t *testing.T) {
	src := &videoSource{}
	a, b := src.frame(0), src.frame(20)
	if bytes.Equal(a.px, b.px) {
		t.Fatal("video frames must animate over time")
	}
}

// End-to-end content integrity: a frame that is encoded, SEALED with the real voice
// crypto, relayed (bytes only), OPENED, and decoded must recover the exact pixels
// AND its embedded frame marker. This is the proof that "actual video" survives the
// real media path — counters going nonzero is not enough.
func TestVideoFrameSurvivesSealOpenDecode(t *testing.T) {
	key := bytes.Repeat([]byte{0x7e}, crypto.KeySize)
	roomID := "room-video"
	var keyID uint8 = 4
	var ssrc uint32 = 909090
	var counter uint64 = 5

	src := &videoSource{}
	const frameIdx = 12345
	orig := src.frame(frameIdx)
	payload := encodeFrame(orig)

	// seal exactly as the send path does: 24-byte header as AAD.
	hdr := protocol.MediaHeader{
		Type: protocol.PacketTypeVideo, Codec: protocol.CodecH264,
		Flags: protocol.FlagKeyframe, KeyID: keyID, SSRC: ssrc, Counter: counter,
	}
	aad := hdr.Marshal()
	sc, err := crypto.NewSessionCryptoDerived(key, roomID, keyID)
	if err != nil {
		t.Fatal(err)
	}
	ct := sc.EncryptSSRC(aad, payload, counter, ssrc)

	// receive path: open with the shared room key + per-SSRC nonce base.
	cipher, err := crypto.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	base := crypto.DeriveNonceBase(key, roomID, keyID, ssrc)
	pt, err := cipher.OpenWithBase(aad, ct, base, counter)
	if err != nil {
		t.Fatalf("monitor open failed: %v", err)
	}
	dec, err := decodeFrame(pt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(dec.px, orig.px) {
		t.Fatal("recovered pixels differ from sent frame")
	}
	if got := frameMarker(dec); got != frameIdx&0xFFFF {
		t.Fatalf("frame marker = %d, want %d (integrity broken)", got, frameIdx&0xFFFF)
	}
}

// decodeFrame must reject malformed input rather than panic.
func TestDecodeFrameRejectsGarbage(t *testing.T) {
	if _, err := decodeFrame([]byte{}); err == nil {
		t.Fatal("empty input must error")
	}
	if _, err := decodeFrame([]byte{200, 200, 0x00}); err == nil {
		t.Fatal("truncated 200x200 frame must error")
	}
}
