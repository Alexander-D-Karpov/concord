package voiceassign

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool       *pgxpool.Pool
	jwtManager *jwt.Manager
}

type VoiceAssignment struct {
	ServerID    string
	Host        string
	Port        uint32
	Token       string
	KeyID       []byte
	KeyMaterial []byte
	NonceBase   []byte
}

func NewService(pool *pgxpool.Pool, jwtManager *jwt.Manager) *Service {
	return &Service{
		pool:       pool,
		jwtManager: jwtManager,
	}
}

func (s *Service) AssignVoiceServer(ctx context.Context, userID, roomID string) (*VoiceAssignment, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, fmt.Errorf("invalid room_id: %w", err)
	}

	var serverID uuid.UUID
	var addrUDP string

	err = s.pool.QueryRow(ctx, `
		SELECT vs.id, vs.addr_udp
		FROM voice_servers vs
		LEFT JOIN rooms r ON r.voice_server_id = vs.id
		WHERE (r.id = $1 OR r.voice_server_id IS NULL)
		AND vs.status = 'online'
		ORDER BY 
			CASE WHEN r.voice_server_id = vs.id THEN 0 ELSE 1 END,
			vs.load_score ASC
		LIMIT 1
	`, roomUUID).Scan(&serverID, &addrUDP)

	if err == pgx.ErrNoRows {
		err = s.pool.QueryRow(ctx, `
			SELECT id, addr_udp
			FROM voice_servers
			WHERE status = 'online'
			ORDER BY load_score ASC
			LIMIT 1
		`).Scan(&serverID, &addrUDP)
	}

	if err != nil {
		return nil, fmt.Errorf("no available voice server: %w", err)
	}

	host, port := parseUDPAddr(addrUDP)

	voiceToken, err := s.jwtManager.GenerateVoiceToken(userID, roomID, serverID.String(), 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed to generate voice token: %w", err)
	}

	keyID := make([]byte, 4)
	keyMaterial := make([]byte, 32)
	nonceBase := make([]byte, 12)

	if _, err := rand.Read(keyID); err != nil {
		return nil, fmt.Errorf("failed to generate key_id: %w", err)
	}
	if _, err := rand.Read(keyMaterial); err != nil {
		return nil, fmt.Errorf("failed to generate key_material: %w", err)
	}
	if _, err := rand.Read(nonceBase); err != nil {
		return nil, fmt.Errorf("failed to generate nonce_base: %w", err)
	}

	return &VoiceAssignment{
		ServerID:    serverID.String(),
		Host:        host,
		Port:        port,
		Token:       voiceToken,
		KeyID:       keyID,
		KeyMaterial: keyMaterial,
		NonceBase:   nonceBase,
	}, nil
}

func parseUDPAddr(addr string) (string, uint32) {
	var host string
	var port uint32

	_, err := fmt.Sscanf(addr, "%[^:]:%d", &host, &port)
	if err != nil {
		host = addr
		port = 50000
	}

	return host, port
}
