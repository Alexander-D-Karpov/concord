package cdn

import (
	"context"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type CDNServer struct {
	ID           uuid.UUID
	Name         string
	Region       string
	AddrUDP      string
	AddrCtrl     string
	AccessToken  *string
	OwnerUserID  *uuid.UUID
	IsOfficial   bool
	Status       string
	Latency      *int32
	CapacityHint int32
	LoadScore    float64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, cdn *CDNServer) error {
	query := `
		INSERT INTO voice_servers (id, name, region, addr_udp, addr_ctrl, owner_user_id, status, capacity_hint, load_score)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at, updated_at
	`
	if cdn.ID == uuid.Nil {
		cdn.ID = uuid.New()
	}
	return r.pool.QueryRow(ctx, query,
		cdn.ID, cdn.Name, cdn.Region, cdn.AddrUDP, cdn.AddrCtrl,
		cdn.OwnerUserID, cdn.Status, cdn.CapacityHint, cdn.LoadScore,
	).Scan(&cdn.CreatedAt, &cdn.UpdatedAt)
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*CDNServer, error) {
	query := `
		SELECT id, name, region, addr_udp, addr_ctrl, owner_user_id, 
		       status, capacity_hint, load_score, created_at, updated_at
		FROM voice_servers
		WHERE id = $1
	`
	cdn := &CDNServer{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&cdn.ID, &cdn.Name, &cdn.Region, &cdn.AddrUDP, &cdn.AddrCtrl,
		&cdn.OwnerUserID, &cdn.Status, &cdn.CapacityHint, &cdn.LoadScore,
		&cdn.CreatedAt, &cdn.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("CDN server not found")
	}
	cdn.IsOfficial = cdn.OwnerUserID == nil
	return cdn, err
}

func (r *Repository) ListOfficial(ctx context.Context, region *string) ([]*CDNServer, error) {
	query := `
		SELECT id, name, region, addr_udp, addr_ctrl, owner_user_id,
		       status, capacity_hint, load_score, created_at, updated_at
		FROM voice_servers
		WHERE owner_user_id IS NULL AND status = 'online'
	`
	args := []interface{}{}
	if region != nil {
		query += " AND region = $1"
		args = append(args, *region)
	}
	query += " ORDER BY load_score ASC"
	return r.queryList(ctx, query, args...)
}

func (r *Repository) ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]*CDNServer, error) {
	query := `
		SELECT id, name, region, addr_udp, addr_ctrl, owner_user_id,
		       status, capacity_hint, load_score, created_at, updated_at
		FROM voice_servers
		WHERE owner_user_id = $1
		ORDER BY created_at DESC
	`
	return r.queryList(ctx, query, ownerID)
}

func (r *Repository) UpdateStatus(ctx context.Context, id uuid.UUID, status string, loadScore float64, latency *int32) error {
	query := `
		UPDATE voice_servers
		SET status = $2, load_score = $3, updated_at = NOW()
		WHERE id = $1
	`
	result, err := r.pool.Exec(ctx, query, id, status, loadScore)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("CDN server not found")
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID, ownerID *uuid.UUID) error {
	query := `DELETE FROM voice_servers WHERE id = $1`
	args := []interface{}{id}
	if ownerID != nil {
		query += " AND owner_user_id = $2"
		args = append(args, *ownerID)
	}
	result, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("CDN server not found or unauthorized")
	}
	return nil
}

func (r *Repository) queryList(ctx context.Context, query string, args ...interface{}) ([]*CDNServer, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cdns []*CDNServer
	for rows.Next() {
		cdn := &CDNServer{}
		if err := rows.Scan(
			&cdn.ID, &cdn.Name, &cdn.Region, &cdn.AddrUDP, &cdn.AddrCtrl,
			&cdn.OwnerUserID, &cdn.Status, &cdn.CapacityHint, &cdn.LoadScore,
			&cdn.CreatedAt, &cdn.UpdatedAt,
		); err != nil {
			return nil, err
		}
		cdn.IsOfficial = cdn.OwnerUserID == nil
		cdns = append(cdns, cdn)
	}
	return cdns, rows.Err()
}
