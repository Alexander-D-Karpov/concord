package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"testing"

	"golang.org/x/crypto/hkdf"
)

// The server's per-SSRC nonce base must byte-match the client's derivation, or
// it cannot decrypt client media.
func TestDeriveNonceBaseMatchesClient(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeySize)
	roomID := "room-abc"
	var keyID uint8 = 7
	var ssrc uint32 = 2000

	got := DeriveNonceBase(key, roomID, keyID, ssrc)

	// independent client-side reference computation
	info := []byte("nonce-base\x00")
	info = append(info, []byte(roomID)...)
	info = append(info, keyID)
	ss := make([]byte, 4)
	binary.BigEndian.PutUint32(ss, ssrc)
	info = append(info, ss...)
	r := hkdf.New(sha256.New, key, nil, info)
	want := make([]byte, NonceBaseSize)
	_, _ = io.ReadFull(r, want)

	if !bytes.Equal(got[:], want) {
		t.Fatalf("nonce base mismatch: server %x vs client %x", got[:], want)
	}
}

// End-to-end: a client seals with AES-256-GCM + per-SSRC nonce base; the server
// must open it, and a wrong SSRC (different nonce base) must fail.
func TestServerDecryptsClientEncrypted(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, KeySize)
	roomID := "room-xyz"
	var keyID uint8 = 3
	var ssrc uint32 = 2001
	var counter uint64 = 12345

	aad := []byte("aad-24-bytes-header-here!")
	plaintext := []byte("opus frame bytes")

	base := DeriveNonceBase(key, roomID, keyID, ssrc)
	nonce := make([]byte, NonceSize)
	copy(nonce, base[:])
	binary.BigEndian.PutUint64(nonce[NonceBaseSize:], counter)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	ct := gcm.Seal(nil, nonce, plaintext, aad)

	sc, err := NewSessionCryptoDerived(key, roomID, keyID)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := sc.DecryptSSRC(aad, ct, counter, ssrc)
	if err != nil {
		t.Fatalf("server decrypt failed: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Fatalf("plaintext mismatch: %q", pt)
	}

	// Wrong SSRC → different nonce base → must fail. Use a fresh counter so this
	// isolates the nonce-base mismatch from replay protection.
	counter2 := counter + 100
	nonce2 := make([]byte, NonceSize)
	copy(nonce2, base[:])
	binary.BigEndian.PutUint64(nonce2[NonceBaseSize:], counter2)
	ct2 := gcm.Seal(nil, nonce2, plaintext, aad)
	if _, err := sc.DecryptSSRC(aad, ct2, counter2, ssrc+1); err == nil {
		t.Fatal("decrypt with wrong ssrc must fail (different nonce base)")
	}
}

// SealWithBase must be the exact inverse of OpenWithBase: a packet sealed with a
// given nonce base + counter opens back to the same plaintext, and any AAD change
// (the media header is authenticated) makes the open fail.
func TestSealOpenWithBaseRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x22}, KeySize)
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	var base [NonceBaseSize]byte
	copy(base[:], []byte{1, 2, 3, 4})
	var counter uint64 = 999
	aad := []byte("twenty-four-byte-aad-hdr")
	plaintext := []byte("hello media payload")

	ct := c.SealWithBase(aad, plaintext, base, counter)
	if bytes.Equal(ct, plaintext) {
		t.Fatal("ciphertext must differ from plaintext")
	}
	pt, err := c.OpenWithBase(aad, ct, base, counter)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Fatalf("round-trip mismatch: %q", pt)
	}

	badAAD := append([]byte(nil), aad...)
	badAAD[0] ^= 0xff
	if _, err := c.OpenWithBase(badAAD, ct, base, counter); err == nil {
		t.Fatal("open with tampered aad must fail")
	}
}

// EncryptSSRC is the client send path mirror of DecryptSSRC. It must byte-match a
// hand-rolled AES-256-GCM seal over the per-SSRC derived nonce base, so media sent
// by a shared-code test client decrypts on real server/peer receive paths.
func TestEncryptSSRCMatchesManualSeal(t *testing.T) {
	key := bytes.Repeat([]byte{0x33}, KeySize)
	roomID := "room-seal"
	var keyID uint8 = 5
	var ssrc uint32 = 4242
	var counter uint64 = 77

	aad := []byte("aad-header-24-bytes-long")
	plaintext := []byte("raw pcm frame")

	sc, err := NewSessionCryptoDerived(key, roomID, keyID)
	if err != nil {
		t.Fatal(err)
	}
	got := sc.EncryptSSRC(aad, plaintext, counter, ssrc)

	base := DeriveNonceBase(key, roomID, keyID, ssrc)
	nonce := make([]byte, NonceSize)
	copy(nonce, base[:])
	binary.BigEndian.PutUint64(nonce[NonceBaseSize:], counter)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	want := gcm.Seal(nil, nonce, plaintext, aad)

	if !bytes.Equal(got, want) {
		t.Fatalf("EncryptSSRC mismatch:\n got=%x\nwant=%x", got, want)
	}

	rx, err := NewSessionCryptoDerived(key, roomID, keyID)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := rx.DecryptSSRC(aad, got, counter, ssrc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Fatalf("plaintext mismatch: %q", pt)
	}
}
