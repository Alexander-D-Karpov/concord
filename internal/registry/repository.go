package registry

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VoiceServer is a registered voice/media server and its current placement signals.
// SecretHash holds the SHA-256 of its shared secret (nil if unset) and is never
// exposed to clients; LoadScore is the computed rank used for assignment.
type VoiceServer struct {
	ID           uuid.UUID
	Name         string
	Region       string
	AddrUDP      string
	AddrCtrl     string
	Status       string
	CapacityHint int32
	LoadScore    float64
	SecretHash   *string
	JWKSUrl      *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Repository persists voice servers in Postgres.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a Repository backed by the given connection pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Upsert inserts or updates the server by ID, generating a UUID if none is set and
// populating CreatedAt/UpdatedAt from the row. A nil SecretHash preserves the
// existing stored secret (via COALESCE) rather than clearing it.
func (r *Repository) Upsert(ctx context.Context, server *VoiceServer) error {
	query := `
		INSERT INTO voice_servers (id, name, region, addr_udp, addr_ctrl, status, capacity_hint, load_score, secret_hash, jwks_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			region = EXCLUDED.region,
			addr_udp = EXCLUDED.addr_udp,
			addr_ctrl = EXCLUDED.addr_ctrl,
			status = EXCLUDED.status,
			capacity_hint = EXCLUDED.capacity_hint,
			load_score = EXCLUDED.load_score,
			secret_hash = COALESCE(EXCLUDED.secret_hash, voice_servers.secret_hash),
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
		server.SecretHash,
		server.JWKSUrl,
	).Scan(&server.CreatedAt, &server.UpdatedAt)
}

// VerifyServerSecret constant-time compares the SHA-256 of secret against the
// server's stored hash. It returns NotFound for an unknown server, Unauthorized if
// the server has no secret configured or the secret does not match, and Internal on
// a query error.
func (r *Repository) VerifyServerSecret(ctx context.Context, serverID uuid.UUID, secret string) error {
	var storedHash *string
	err := r.pool.QueryRow(ctx,
		`SELECT secret_hash FROM voice_servers WHERE id = $1`, serverID,
	).Scan(&storedHash)
	if err == pgx.ErrNoRows {
		return errors.NotFound("voice server not found")
	}
	if err != nil {
		return errors.Internal("failed to load server secret", err)
	}
	if storedHash == nil {
		return errors.Unauthorized("server has no secret configured")
	}
	if subtle.ConstantTimeCompare([]byte(HashSecret(secret)), []byte(*storedHash)) != 1 {
		return errors.Unauthorized("invalid voice secret")
	}
	return nil
}

// UpdateHeartbeat recomputes the server's load score from the reported metrics and
// marks it online. It returns pgx.ErrNoRows if no server with that ID exists.
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

// List returns online voice servers ordered by ascending load score (least loaded
// first). A non-nil, non-empty region restricts results to that region. The secret
// hash is not selected.
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

// calculateLoadScore combines sessions, CPU, rooms, and egress into a single 0..~1
// placement score (lower is less loaded). Each signal is normalized against a soft
// capacity target and CPU is clamped to 0..1, then weighted (sessions 0.4, cpu 0.3,
// rooms 0.15, bandwidth 0.15) so no raw count dominates the others.
func calculateLoadScore(activeRooms, activeSessions int32, cpu, outboundMbps float64) float64 {
	// Normalize each signal to ~0..1 against soft capacity targets so CPU and
	// egress actually influence placement — previously raw session/room counts
	// dwarfed the 0..1 cpu and the (now real) Mbps rate.
	const (
		sessionTarget = 1000.0 // ~VOICE_CAPACITY
		roomTarget    = 200.0
		mbpsTarget    = 1000.0 // ~1 Gbps line
	)
	if cpu < 0 {
		cpu = 0
	}
	if cpu > 1 {
		cpu = 1
	}
	sessionLoad := float64(activeSessions) / sessionTarget
	roomLoad := float64(activeRooms) / roomTarget
	bwLoad := outboundMbps / mbpsTarget

	return sessionLoad*0.4 + cpu*0.3 + roomLoad*0.15 + bwLoad*0.15
}
