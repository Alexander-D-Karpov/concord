package jwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type TokenType string

const (
	TokenTypeAccess  TokenType = "access"
	TokenTypeRefresh TokenType = "refresh"
	TokenTypeVoice   TokenType = "voice"
)

type Claims struct {
	UserID    string    `json:"user_id"`
	Handle    string    `json:"handle"`
	TokenType TokenType `json:"token_type"`
	RoomID    string    `json:"room_id,omitempty"`
	ServerID  string    `json:"server_id,omitempty"`
	jwt.RegisteredClaims
}

type Manager struct {
	secret      []byte
	voiceSecret []byte
}

func NewManager(secret, voiceSecret string) *Manager {
	return &Manager{
		secret:      []byte(secret),
		voiceSecret: []byte(voiceSecret),
	}
}

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

func (m *Manager) ValidateAccessToken(tokenString string) (*Claims, error) {
	return m.validateToken(tokenString, m.secret, TokenTypeAccess)
}

func (m *Manager) ValidateRefreshToken(tokenString string) (*Claims, error) {
	return m.validateToken(tokenString, m.secret, TokenTypeRefresh)
}

func (m *Manager) ValidateVoiceToken(tokenString string) (*Claims, error) {
	return m.validateToken(tokenString, m.voiceSecret, TokenTypeVoice)
}

func (m *Manager) validateToken(tokenString string, secret []byte, expectedType TokenType) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return secret, nil
	})

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
