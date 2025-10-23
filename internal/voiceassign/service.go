package voiceassign

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type VoiceServer struct {
	ID        uuid.UUID
	Name      string
	Region    string
	AddrUDP   string
	AddrCtrl  string
	Status    string
	LoadScore float64
}

type Service struct {
	pool       *pgxpool.Pool
	jwtManager *jwt.Manager
}

func NewService(pool *pgxpool.Pool, jwtManager *jwt.Manager) *Service {
	return &Service{
		pool:       pool,
		jwtManager: jwtManager,
	}
}

func (s *Service) SelectServerForRoom(ctx context.Context, roomID string, region *string) (*VoiceServer, error) {
	query := `
		SELECT id, name, region, addr_udp, addr_ctrl, status, load_score
		FROM voice_servers
		WHERE status = 'online'
	`
	args := []interface{}{}

	if region != nil && *region != "" {
		query += " AND region = $1"
		args = append(args, *region)
	}

	query += " ORDER BY load_score ASC LIMIT 1"

	var server VoiceServer
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&server.ID,
		&server.Name,
		&server.Region,
		&server.AddrUDP,
		&server.AddrCtrl,
		&server.Status,
		&server.LoadScore,
	)

	if err != nil {
		return nil, errors.NotFound("no available voice servers")
	}

	return &server, nil
}

func (s *Service) IssueJoinToken(userID, roomID, serverID string, duration time.Duration) (string, []byte, error) {
	token, err := s.jwtManager.GenerateVoiceToken(userID, roomID, serverID, duration)
	if err != nil {
		return "", nil, err
	}

	key, err := crypto.GenerateKey()
	if err != nil {
		return "", nil, err
	}

	return token, key, nil
}

func (s *Service) GetServerByID(ctx context.Context, serverID string) (*VoiceServer, error) {
	id, err := uuid.Parse(serverID)
	if err != nil {
		return nil, errors.BadRequest("invalid server id")
	}

	query := `
		SELECT id, name, region, addr_udp, addr_ctrl, status, load_score
		FROM voice_servers
		WHERE id = $1
	`

	var server VoiceServer
	err = s.pool.QueryRow(ctx, query, id).Scan(
		&server.ID,
		&server.Name,
		&server.Region,
		&server.AddrUDP,
		&server.AddrCtrl,
		&server.Status,
		&server.LoadScore,
	)

	if err != nil {
		return nil, errors.NotFound("voice server not found")
	}

	return &server, nil
}
