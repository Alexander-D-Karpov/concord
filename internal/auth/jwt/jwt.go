package jwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TokenType names the class of a token. It is embedded in the claims and checked
// on validation so a token minted for one purpose cannot be replayed as another.
type TokenType string

// clockSkewLeeway tolerates small clock differences between clients (esp. mobile)
// and the server when validating iat/nbf/exp.
const clockSkewLeeway = 30 * time.Second

const (
	// TokenTypeAccess is a short-lived bearer token for authenticating API calls,
	// signed with the main secret.
	TokenTypeAccess TokenType = "access"
	// TokenTypeRefresh is a long-lived token used only to mint new access tokens,
	// signed with the main secret and tracked (hashed) server-side for revocation.
	TokenTypeRefresh TokenType = "refresh"
	// TokenTypeVoice authorizes joining a specific voice room/server; it is signed
	// with the separate voice secret and carries RoomID/ServerID.
	TokenTypeVoice TokenType = "voice"
)

// Claims is the JWT payload carried by every Concord token. RoomID and ServerID
// are populated only for voice tokens; TokenType is enforced on validation.
type Claims struct {
	UserID    string    `json:"user_id"`
	Handle    string    `json:"handle"`
	TokenType TokenType `json:"token_type"`
	RoomID    string    `json:"room_id,omitempty"`
	ServerID  string    `json:"server_id,omitempty"`
	jwt.RegisteredClaims
}

// Manager signs and validates tokens. It holds two independent HMAC secrets:
// secret for access/refresh tokens and voiceSecret for voice tokens, so the two
// token families cannot be forged or replayed across each other.
type Manager struct {
	secret      []byte
	voiceSecret []byte
}

// NewManager returns a Manager keyed with the given main and voice secrets. Both
// are used as raw HMAC-SHA256 keys; they must be non-empty and kept confidential.
func NewManager(secret, voiceSecret string) *Manager {
	return &Manager{
		secret:      []byte(secret),
		voiceSecret: []byte(voiceSecret),
	}
}

// GenerateAccessToken mints an HS256 access token for the user, signed with the
// main secret, embedding the handle and expiring after duration. Each token gets
// a fresh JTI, issuer "concord-api", and audience "concord".
func (m *Manager) GenerateAccessToken(userID, handle string, duration time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:    userID,
		Handle:    handle,
		TokenType: TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.New().String(),
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(duration)),
			Issuer:    "concord-api",
			Audience:  []string{"concord"},
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

// GenerateRefreshToken mints an HS256 refresh token for the user, signed with the
// main secret and expiring after duration. It carries no handle; the caller is
// expected to persist its hash server-side for rotation and revocation.
func (m *Manager) GenerateRefreshToken(userID string, duration time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:    userID,
		TokenType: TokenTypeRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.New().String(),
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(duration)),
			Issuer:    "concord-api",
			Audience:  []string{"concord"},
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

// GenerateVoiceToken mints an HS256 voice token scoped to the given room and
// server, signed with the separate voiceSecret (not the main secret) and with
// audience "concord-voice". This isolates voice authorization from API access.
func (m *Manager) GenerateVoiceToken(userID, roomID, serverID string, duration time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:    userID,
		TokenType: TokenTypeVoice,
		RoomID:    roomID,
		ServerID:  serverID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.New().String(),
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(duration)),
			Issuer:    "concord-api",
			Audience:  []string{"concord-voice"},
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.voiceSecret)
}

// ValidateAccessToken parses and verifies tokenString against the main secret and
// requires TokenType access; it errors on a bad signature, expiry, or wrong type.
func (m *Manager) ValidateAccessToken(tokenString string) (*Claims, error) {
	return m.validateToken(tokenString, m.secret, TokenTypeAccess)
}

// ValidateRefreshToken parses and verifies tokenString against the main secret and
// requires TokenType refresh, so an access token cannot be used to refresh.
func (m *Manager) ValidateRefreshToken(tokenString string) (*Claims, error) {
	return m.validateToken(tokenString, m.secret, TokenTypeRefresh)
}

// ValidateVoiceToken parses and verifies tokenString against the voice secret and
// requires TokenType voice; a token signed with the main secret will not validate.
func (m *Manager) ValidateVoiceToken(tokenString string) (*Claims, error) {
	return m.validateToken(tokenString, m.voiceSecret, TokenTypeVoice)
}

// validateToken parses tokenString with the given secret, rejecting any non-HMAC
// signing method (guarding against algorithm-substitution attacks), then confirms
// the token is valid and its TokenType matches expectedType before returning the
// claims. Returns an error rather than partial claims on any failure.
func (m *Manager) validateToken(tokenString string, secret []byte, expectedType TokenType) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return secret, nil
	}, jwt.WithLeeway(clockSkewLeeway))

	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}

	if claims.TokenType != expectedType {
		return nil, errors.New("invalid token type")
	}

	return claims, nil
}
