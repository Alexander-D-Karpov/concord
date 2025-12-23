package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

var (
	ErrInvalidKey    = errors.New("invalid key size: expected 32 bytes")
	ErrDecryptFailed = errors.New("decryption failed")
	ErrReplay        = errors.New("replay/duplicate packet")
	ErrInvalidNonce  = errors.New("invalid nonce size")
)

const (
	NonceSize     = 12
	NonceBaseSize = 4
	KeySize       = 32
	AuthTagSize   = 16
	ReplayWindow  = 256
)

type Cipher struct {
	aead      cipher.AEAD
	nonceBase [NonceBaseSize]byte
}

func NewCipher(key []byte, nonceBase []byte) (*Cipher, error) {
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

	c := &Cipher{aead: aead}
	if len(nonceBase) >= NonceBaseSize {
		copy(c.nonceBase[:], nonceBase[:NonceBaseSize])
	}

	return c, nil
}

func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

func GenerateNonceBase() ([]byte, error) {
	nonce := make([]byte, NonceBaseSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return nonce, nil
}

func RandomCounterStart() (uint64, error) {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b[:]), nil
}

func (c *Cipher) deriveNonce(counter uint64) []byte {
	nonce := make([]byte, NonceSize)
	copy(nonce[0:NonceBaseSize], c.nonceBase[:])
	binary.BigEndian.PutUint64(nonce[NonceBaseSize:], counter)
	return nonce
}

func (c *Cipher) Seal(aad, plaintext []byte, counter uint64) []byte {
	nonce := c.deriveNonce(counter)
	return c.aead.Seal(nil, nonce, plaintext, aad)
}

func (c *Cipher) Open(aad, ciphertext []byte, counter uint64) ([]byte, error) {
	nonce := c.deriveNonce(counter)
	pt, err := c.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return pt, nil
}

func DeriveNonceFromParams(ssrc, timestamp uint32, sequence uint16) []byte {
	nonce := make([]byte, NonceSize)
	binary.BigEndian.PutUint32(nonce[0:4], ssrc)
	binary.BigEndian.PutUint32(nonce[4:8], timestamp)
	binary.BigEndian.PutUint16(nonce[8:10], sequence)
	return nonce
}

type ReplayFilter struct {
	mu     sync.Mutex
	max    uint64
	bitmap [4]uint64
	inited bool
}

func NewReplayFilter() *ReplayFilter {
	return &ReplayFilter{}
}

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

type SessionCrypto struct {
	Cipher       *Cipher
	ReplayFilter *ReplayFilter
	KeyID        uint8
	Counter      uint64
	mu           sync.Mutex
}

func NewSessionCrypto(key, nonceBase []byte, keyID uint8) (*SessionCrypto, error) {
	cipher, err := NewCipher(key, nonceBase)
	if err != nil {
		return nil, err
	}

	counter, err := RandomCounterStart()
	if err != nil {
		counter = 0
	}

	return &SessionCrypto{
		Cipher:       cipher,
		ReplayFilter: NewReplayFilter(),
		KeyID:        keyID,
		Counter:      counter,
	}, nil
}

func (sc *SessionCrypto) NextCounter() uint64 {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	c := sc.Counter
	sc.Counter++
	return c
}

func (sc *SessionCrypto) Encrypt(aad, plaintext []byte) ([]byte, uint64) {
	counter := sc.NextCounter()
	ciphertext := sc.Cipher.Seal(aad, plaintext, counter)
	return ciphertext, counter
}

func (sc *SessionCrypto) Decrypt(aad, ciphertext []byte, counter uint64) ([]byte, error) {
	if err := sc.ReplayFilter.Check(counter); err != nil {
		return nil, err
	}
	return sc.Cipher.Open(aad, ciphertext, counter)
}
