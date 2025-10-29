package registry

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type VoiceServer struct {
	ID           uuid.UUID
	Name         string
	Region       string
	AddrUDP      string
	AddrCtrl     string
	Status       string
	CapacityHint int32
	LoadScore    float64
	SharedSecret *string
	JWKSUrl      *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Upsert(ctx context.Context, server *VoiceServer) error {
	query := `
		INSERT INTO voice_servers (id, name, region, addr_udp, addr_ctrl, status, capacity_hint, load_score, shared_secret, jwks_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			region = EXCLUDED.region,
			addr_udp = EXCLUDED.addr_udp,
			addr_ctrl = EXCLUDED.addr_ctrl,
			status = EXCLUDED.status,
			capacity_hint = EXCLUDED.capacity_hint,
			load_score = EXCLUDED.load_score,
			shared_secret = EXCLUDED.shared_secret,
			jwks_url = EXCLUDED.jwks_url,
			updated_at = NOW()
		RETURNING created_at, updated_at
	`

	if server.ID == uuid.Nil {
		server.ID = uuid.New()
	}

	return r.pool.QueryRow(ctx, query,
		server.ID,
		server.Name,
		server.Region,
		server.AddrUDP,
		server.AddrCtrl,
		server.Status,
		server.CapacityHint,
		server.LoadScore,
		server.SharedSecret,
		server.JWKSUrl,
	).Scan(&server.CreatedAt, &server.UpdatedAt)
}

func (r *Repository) UpdateHeartbeat(ctx context.Context, serverID uuid.UUID, activeRooms, activeSessions int32, cpu, outboundMbps float64) error {
	loadScore := calculateLoadScore(activeRooms, activeSessions, cpu, outboundMbps)

	query := `
		UPDATE voice_servers
		SET load_score = $2, status = 'online', updated_at = NOW()
		WHERE id = $1
	`

	result, err := r.pool.Exec(ctx, query, serverID, loadScore)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	return nil
}

func (r *Repository) List(ctx context.Context, region *string) ([]*VoiceServer, error) {
	query := `
		SELECT id, name, region, addr_udp, addr_ctrl, status, capacity_hint, load_score, created_at, updated_at
		FROM voice_servers
		WHERE status = 'online'
	`

	args := []interface{}{}
	if region != nil && *region != "" {
		query += " AND region = $1"
		args = append(args, *region)
	}

	query += " ORDER BY load_score ASC"

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []*VoiceServer
	for rows.Next() {
		server := &VoiceServer{}
		if err := rows.Scan(
			&server.ID,
			&server.Name,
			&server.Region,
			&server.AddrUDP,
			&server.AddrCtrl,
			&server.Status,
			&server.CapacityHint,
			&server.LoadScore,
			&server.CreatedAt,
			&server.UpdatedAt,
		); err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}

	return servers, rows.Err()
}

func calculateLoadScore(activeRooms, activeSessions int32, cpu, outboundMbps float64) float64 {
	sessionWeight := float64(activeSessions) * 0.4
	roomWeight := float64(activeRooms) * 0.3
	cpuWeight := cpu * 0.2
	bandwidthWeight := outboundMbps * 0.1

	return sessionWeight + roomWeight + cpuWeight + bandwidthWeight
}
