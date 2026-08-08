package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"golang.org/x/crypto/hkdf"
)

var (
	// ErrInvalidKey is returned by NewCipher when the key is not KeySize bytes.
	ErrInvalidKey = errors.New("invalid key size: expected 32 bytes")
	// ErrDecryptFailed is returned when GCM authentication/decryption fails. It is
	// deliberately opaque (no detail on why) to avoid leaking oracle information.
	ErrDecryptFailed = errors.New("decryption failed")
	// ErrReplay is returned when the replay filter rejects a packet whose counter
	// was already seen or has fallen behind the sliding window.
	ErrReplay = errors.New("replay/duplicate packet")
)

const (
	// NonceSize is the full GCM nonce length: NonceBaseSize base bytes plus an
	// 8-byte big-endian counter.
	NonceSize = 12
	// NonceBaseSize is the per-SSRC prefix (derived via HKDF) of each nonce.
	NonceBaseSize = 4
	// KeySize is the required AES-256 key length in bytes.
	KeySize = 32
	// AuthTagSize is the GCM authentication tag appended to every ciphertext.
	AuthTagSize = 16
	// ReplayWindow is the number of counter positions tracked behind the highest
	// seen counter; anything older is rejected as a replay.
	ReplayWindow = 256
)

// Cipher wraps an AES-256-GCM AEAD. It is safe for concurrent use because the
// nonce is supplied per call rather than held as state.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds an AES-256-GCM Cipher from a 32-byte key, returning
// ErrInvalidKey if the key length is wrong.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// OpenWithBase opens a packet using an explicit per-stream nonce (nonceBase ‖
// counter, 12 bytes total).
func (c *Cipher) OpenWithBase(aad, ciphertext []byte, nonceBase [NonceBaseSize]byte, counter uint64) ([]byte, error) {
	nonce := make([]byte, NonceSize)
	copy(nonce[0:NonceBaseSize], nonceBase[:])
	binary.BigEndian.PutUint64(nonce[NonceBaseSize:], counter)
	pt, err := c.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return pt, nil
}

// SealWithBase is the send-side inverse of OpenWithBase: it seals plaintext under
// the same explicit per-stream nonce (nonceBase ‖ counter, 12 bytes total),
// authenticating aad. It is the sender counterpart used by clients (and the shared
// test harness) so encrypt/decrypt stay in one place and cannot drift.
func (c *Cipher) SealWithBase(aad, plaintext []byte, nonceBase [NonceBaseSize]byte, counter uint64) []byte {
	nonce := make([]byte, NonceSize)
	copy(nonce[0:NonceBaseSize], nonceBase[:])
	binary.BigEndian.PutUint64(nonce[NonceBaseSize:], counter)
	return c.aead.Seal(nil, nonce, plaintext, aad)
}

// DeriveNonceBase computes a per-SSRC nonce base via HKDF-SHA256, matching the
// client: info = "nonce-base\x00" || room_id || key_id || ssrc(BE). It is
// distinct per SSRC, so two senders can never collide on a (nonce_base, counter)
// pair — removing the shared-nonce-base GCM reuse risk.
func DeriveNonceBase(key []byte, roomID string, keyID uint8, ssrc uint32) [NonceBaseSize]byte {
	info := make([]byte, 0, len("nonce-base\x00")+len(roomID)+1+4)
	info = append(info, []byte("nonce-base\x00")...)
	info = append(info, []byte(roomID)...)
	info = append(info, keyID)
	var ss [4]byte
	binary.BigEndian.PutUint32(ss[:], ssrc)
	info = append(info, ss[:]...)

	reader := hkdf.New(sha256.New, key, nil, info)
	var out [NonceBaseSize]byte
	_, _ = io.ReadFull(reader, out[:])
	return out
}

// ReplayFilter is a sliding-window anti-replay filter over 64-bit packet
// counters. The bitmap tracks which of the ReplayWindow positions below max have
// already been seen. It is concurrency-safe via its own mutex.
type ReplayFilter struct {
	mu     sync.Mutex
	max    uint64
	bitmap [4]uint64
	inited bool
}

// NewReplayFilter returns an empty filter; the first Check seeds max with that
// packet's counter.
func NewReplayFilter() *ReplayFilter {
	return &ReplayFilter{}
}

// Check records counter and reports whether it is fresh. It returns ErrReplay if
// the counter was already seen or lies more than ReplayWindow behind the highest
// counter; nil means accepted (and the position is now marked used). Safe for
// concurrent callers.
func (rf *ReplayFilter) Check(counter uint64) error {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if !rf.inited {
		rf.inited = true
		rf.max = counter
		rf.bitmap[0] = 1
		return nil
	}

	if counter > rf.max {
		shift := counter - rf.max
		rf.shiftWindow(shift)
		rf.max = counter
		rf.bitmap[0] |= 1
		return nil
	}

	diff := rf.max - counter
	if diff >= ReplayWindow {
		return ErrReplay
	}

	word := diff / 64
	bit := diff % 64
	mask := uint64(1) << bit

	if (rf.bitmap[word] & mask) != 0 {
		return ErrReplay
	}

	rf.bitmap[word] |= mask
	return nil
}

// shiftWindow advances the window by shift positions when a newer counter
// arrives, shifting the seen-bitmap toward older bits (and zero-filling the
// freshly exposed high positions). Caller must hold rf.mu.
func (rf *ReplayFilter) shiftWindow(shift uint64) {
	if shift >= ReplayWindow {
		for i := range rf.bitmap {
			rf.bitmap[i] = 0
		}
		return
	}

	whole := int(shift / 64)
	bits := shift % 64

	if whole > 0 {
		for i := len(rf.bitmap) - 1; i >= 0; i-- {
			src := i - whole
			if src >= 0 {
				rf.bitmap[i] = rf.bitmap[src]
			} else {
				rf.bitmap[i] = 0
			}
		}
	}

	if bits == 0 {
		return
	}

	for i := len(rf.bitmap) - 1; i >= 0; i-- {
		var carry uint64
		if i > 0 {
			carry = rf.bitmap[i-1] << (64 - bits)
		}
		rf.bitmap[i] = (rf.bitmap[i] >> bits) | carry
	}
}

// SessionCrypto bundles a session's AEAD cipher, its replay filter, and the
// material (key + roomID + KeyID) needed to re-derive per-SSRC nonce bases at
// decrypt time. KeyID identifies this key on the wire so a rotating peer's
// packets can be routed to the right cipher.
type SessionCrypto struct {
	Cipher       *Cipher
	ReplayFilter *ReplayFilter
	KeyID        uint8
	key          []byte
	roomID       string
}

// NewSessionCryptoDerived builds session crypto that derives the nonce base
// per-SSRC (HKDF) at decrypt time, using the shared room key as the AES key.
func NewSessionCryptoDerived(key []byte, roomID string, keyID uint8) (*SessionCrypto, error) {
	c, err := NewCipher(key)
	if err != nil {
		return nil, err
	}
	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	return &SessionCrypto{
		Cipher:       c,
		ReplayFilter: NewReplayFilter(),
		KeyID:        keyID,
		key:          keyCopy,
		roomID:       roomID,
	}, nil
}

// DecryptSSRC opens a packet using the per-SSRC derived nonce base, with replay
// protection — a replayed packet must not be accepted, since the migration
// verify uses a successful decrypt to move a session's address binding.
func (sc *SessionCrypto) DecryptSSRC(aad, ciphertext []byte, counter uint64, ssrc uint32) ([]byte, error) {
	if err := sc.ReplayFilter.Check(counter); err != nil {
		return nil, err
	}
	base := DeriveNonceBase(sc.key, sc.roomID, sc.KeyID, ssrc)
	return sc.Cipher.OpenWithBase(aad, ciphertext, base, counter)
}

// EncryptSSRC is the send-side mirror of DecryptSSRC: it seals plaintext under the
// per-SSRC derived nonce base, so a client (or the shared test harness) produces
// exactly what a peer's DecryptSSRC expects. There is no replay filter on the send
// path — counters are assigned by the caller.
func (sc *SessionCrypto) EncryptSSRC(aad, plaintext []byte, counter uint64, ssrc uint32) []byte {
	base := DeriveNonceBase(sc.key, sc.roomID, sc.KeyID, ssrc)
	return sc.Cipher.SealWithBase(aad, plaintext, base, counter)
}
