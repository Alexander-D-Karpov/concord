package rooms

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Room struct {
	ID            uuid.UUID
	Name          string
	CreatedBy     uuid.UUID
	VoiceServerID *uuid.UUID
	Region        *string
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

type Member struct {
	RoomID   uuid.UUID
	UserID   uuid.UUID
	Role     string
	JoinedAt time.Time
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, room *Room) error {
	query := `
		INSERT INTO rooms (id, name, created_by, voice_server_id, region)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at
	`

	if room.ID == uuid.Nil {
		room.ID = uuid.New()
	}

	err := r.pool.QueryRow(ctx, query,
		room.ID,
		room.Name,
		room.CreatedBy,
		room.VoiceServerID,
		room.Region,
	).Scan(&room.CreatedAt)

	return err
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*Room, error) {
	query := `
		SELECT id, name, created_by, voice_server_id, region, created_at, deleted_at
		FROM rooms
		WHERE id = $1 AND deleted_at IS NULL
	`

	room := &Room{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&room.ID,
		&room.Name,
		&room.CreatedBy,
		&room.VoiceServerID,
		&room.Region,
		&room.CreatedAt,
		&room.DeletedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("room not found")
	}
	if err != nil {
		return nil, err
	}

	return room, nil
}

func (r *Repository) ListForUser(ctx context.Context, userID uuid.UUID) ([]*Room, error) {
	query := `
		SELECT r.id, r.name, r.created_by, r.voice_server_id, r.region, r.created_at, r.deleted_at
		FROM rooms r
		JOIN memberships m ON r.id = m.room_id
		WHERE m.user_id = $1 AND r.deleted_at IS NULL
		ORDER BY r.created_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rooms []*Room
	for rows.Next() {
		room := &Room{}
		if err := rows.Scan(
			&room.ID,
			&room.Name,
			&room.CreatedBy,
			&room.VoiceServerID,
			&room.Region,
			&room.CreatedAt,
			&room.DeletedAt,
		); err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}

	return rooms, rows.Err()
}

func (r *Repository) UpdateVoiceServer(ctx context.Context, roomID, serverID uuid.UUID) error {
	query := `
		UPDATE rooms
		SET voice_server_id = $2
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query, roomID, serverID)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("room not found")
	}

	return nil
}

func (r *Repository) AddMember(ctx context.Context, roomID, userID uuid.UUID, role string) error {
	query := `
		INSERT INTO memberships (room_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (room_id, user_id) DO NOTHING
	`

	_, err := r.pool.Exec(ctx, query, roomID, userID, role)
	return err
}

func (r *Repository) RemoveMember(ctx context.Context, roomID, userID uuid.UUID) error {
	query := `DELETE FROM memberships WHERE room_id = $1 AND user_id = $2`
	_, err := r.pool.Exec(ctx, query, roomID, userID)
	return err
}

func (r *Repository) GetMember(ctx context.Context, roomID, userID uuid.UUID) (*Member, error) {
	query := `
		SELECT room_id, user_id, role, joined_at
		FROM memberships
		WHERE room_id = $1 AND user_id = $2
	`

	member := &Member{}
	err := r.pool.QueryRow(ctx, query, roomID, userID).Scan(
		&member.RoomID,
		&member.UserID,
		&member.Role,
		&member.JoinedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("member not found")
	}
	if err != nil {
		return nil, err
	}

	return member, nil
}

func (r *Repository) ListMembers(ctx context.Context, roomID uuid.UUID) ([]*Member, error) {
	query := `
		SELECT room_id, user_id, role, joined_at
		FROM memberships
		WHERE room_id = $1
		ORDER BY joined_at ASC
	`

	rows, err := r.pool.Query(ctx, query, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*Member
	for rows.Next() {
		member := &Member{}
		if err := rows.Scan(
			&member.RoomID,
			&member.UserID,
			&member.Role,
			&member.JoinedAt,
		); err != nil {
			return nil, err
		}
		members = append(members, member)
	}

	return members, rows.Err()
}

func (r *Repository) UpdateMemberRole(ctx context.Context, roomID, userID uuid.UUID, role string) error {
	query := `
		UPDATE memberships
		SET role = $3
		WHERE room_id = $1 AND user_id = $2
	`

	result, err := r.pool.Exec(ctx, query, roomID, userID, role)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("member not found")
	}

	return nil
}
