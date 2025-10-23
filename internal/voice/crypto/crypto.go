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

func (c *Cipher) Encrypt(plaintext []byte, sequence uint32, ssrc uint32) ([]byte, error) {
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	binary.BigEndian.PutUint32(nonce[0:4], sequence)
	binary.BigEndian.PutUint32(nonce[4:8], ssrc)

	ciphertext := c.aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nil
}

func (c *Cipher) Decrypt(ciphertext []byte, sequence uint32, ssrc uint32) ([]byte, error) {
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	binary.BigEndian.PutUint32(nonce[0:4], sequence)
	binary.BigEndian.PutUint32(nonce[4:8], ssrc)

	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrDecryptFailed
	}

	return plaintext, nil
}
