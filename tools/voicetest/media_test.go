package main

import (
	"bytes"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
)

// A packet assembled by the send path must be decryptable by a peer using only the
// shared room key + SSRC + counter. This proves the harness speaks the exact wire
// format real receivers expect: a 24-byte protocol.MediaHeader as AAD, a per-SSRC
// derived nonce, and AES-256-GCM — all from the shared internal/voice packages.
func TestMediaPktDecryptsOnRealReceivePath(t *testing.T) {
	key := bytes.Repeat([]byte{0x5a}, crypto.KeySize)
	roomID := "room-xyz"
	var keyID byte = 9
	var ssrc uint32 = 123456
	var ctr uint64 = 42
	payload := []byte("real pcm/rgb payload bytes")

	sc, err := crypto.NewSessionCryptoDerived(key, roomID, keyID)
	if err != nil {
		t.Fatal(err)
	}
	b := &bot{roomID: roomID, keyMaterial: key, keyID: keyID, sc: sc}

	pkt := b.mediaPkt(protocol.PacketTypeAudio, 0, protocol.CodecOpus, ssrc, 7, 960, ctr, payload)

	if len(pkt) < protocol.MediaHeaderSize {
		t.Fatalf("packet too small: %d", len(pkt))
	}
	hdr, err := protocol.ParseMediaHeader(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Type != protocol.PacketTypeAudio || hdr.Codec != protocol.CodecOpus ||
		hdr.SSRC != ssrc || hdr.Sequence != 7 || hdr.Counter != ctr || hdr.KeyID != keyID {
		t.Fatalf("header mismatch: %+v", hdr)
	}

	aad := pkt[:protocol.MediaHeaderSize]
	ct := pkt[protocol.MediaHeaderSize:]

	rx, err := crypto.NewSessionCryptoDerived(key, roomID, keyID)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := rx.DecryptSSRC(aad, ct, ctr, ssrc)
	if err != nil {
		t.Fatalf("peer decrypt failed: %v", err)
	}
	if !bytes.Equal(pt, payload) {
		t.Fatalf("payload mismatch: got %q want %q", pt, payload)
	}
}

// With no session crypto (server issued a non-32-byte key), the send path must fall
// back to plaintext rather than crash — preserving the prior behavior.
func TestMediaPktPlaintextFallback(t *testing.T) {
	payload := []byte("unencrypted")
	b := &bot{roomID: "r", sc: nil}
	pkt := b.mediaPkt(protocol.PacketTypeAudio, 0, protocol.CodecOpus, 1, 0, 0, 0, payload)
	if !bytes.Equal(pkt[protocol.MediaHeaderSize:], payload) {
		t.Fatal("expected plaintext payload passthrough when sc is nil")
	}
}
