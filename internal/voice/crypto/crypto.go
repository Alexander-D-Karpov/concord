package crypto

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

var (
	ErrEncryptFailed = errors.New("encryption failed")
	ErrDecryptFailed = errors.New("decryption failed")
	ErrInvalidNonce  = errors.New("invalid nonce size")
)

type Cipher struct {
	aead cipher.AEAD
}

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, errors.New("invalid key size")
	}

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}

	return &Cipher{aead: aead}, nil
}

func GenerateKey() ([]byte, error) {
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func GenerateNonceBase() ([]byte, error) {
	nonce := make([]byte, 4)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return nonce, nil
}

func DeriveNonce(ssrc, timestamp uint32, sequence uint16) []byte {
	nonce := make([]byte, chacha20poly1305.NonceSizeX)

	binary.BigEndian.PutUint32(nonce[0:4], ssrc)
	binary.BigEndian.PutUint32(nonce[4:8], timestamp)
	binary.BigEndian.PutUint16(nonce[8:10], sequence)

	return nonce
}

func (c *Cipher) Encrypt(plaintext []byte, ssrc, timestamp uint32, sequence uint16) ([]byte, error) {
	nonce := DeriveNonce(ssrc, timestamp, sequence)

	ciphertext := c.aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nil
}

func (c *Cipher) Decrypt(ciphertext []byte, ssrc, timestamp uint32, sequence uint16) ([]byte, error) {
	nonce := DeriveNonce(ssrc, timestamp, sequence)

	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrDecryptFailed
	}

	return plaintext, nil
}

func (c *Cipher) EncryptWithNonce(plaintext []byte, nonce []byte) ([]byte, error) {
	if len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, ErrInvalidNonce
	}

	ciphertext := c.aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nil
}

func (c *Cipher) DecryptWithNonce(ciphertext []byte, nonce []byte) ([]byte, error) {
	if len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, ErrInvalidNonce
	}

	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrDecryptFailed
	}

	return plaintext, nil
}
