package jwt

import (
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

// TestValidateTokenToleratesClockSkew signs a token whose issued-at/not-before is
// slightly in the future (as a client with a fast clock would produce) and asserts
// it still validates, thanks to the parser leeway.
func TestValidateTokenToleratesClockSkew(t *testing.T) {
	m := NewManager("test-secret", "voice-secret")

	future := time.Now().Add(20 * time.Second)
	claims := &Claims{
		UserID:    "u1",
		Handle:    "u1",
		TokenType: TokenTypeAccess,
		RegisteredClaims: gojwt.RegisteredClaims{
			IssuedAt:  gojwt.NewNumericDate(future),
			NotBefore: gojwt.NewNumericDate(future),
			ExpiresAt: gojwt.NewNumericDate(future.Add(time.Hour)),
		},
	}
	signed, err := gojwt.NewWithClaims(gojwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.ValidateAccessToken(signed); err != nil {
		t.Fatalf("expected token within leeway to validate, got %v", err)
	}
}
