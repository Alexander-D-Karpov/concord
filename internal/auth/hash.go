package auth

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashToken returns the hex-encoded SHA-256 of a refresh token. Refresh tokens are
// only ever persisted and looked up by this hash, never in plaintext.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
