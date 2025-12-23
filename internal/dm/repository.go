package dm

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DMChannel struct {
	ID        uuid.UUID
	User1ID   uuid.UUID
	User2ID   uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

type DMChannelWithUser struct {
	Channel          *DMChannel
	OtherUserID      uuid.UUID
	OtherUserHandle  string
	OtherUserDisplay string
	OtherUserAvatar  string
	OtherUserStatus  string
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) GetOrCreate(ctx context.Context, user1ID, user2ID uuid.UUID) (*DMChannel, error) {
	if user1ID.String() > user2ID.String() {
		user1ID, user2ID = user2ID, user1ID
	}

	query := `
		SELECT id, user1_id, user2_id, created_at, updated_at
		FROM dm_channels
		WHERE user1_id = $1 AND user2_id = $2
	`

	channel := &DMChannel{}
	err := r.pool.QueryRow(ctx, query, user1ID, user2ID).Scan(
		&channel.ID,
		&channel.User1ID,
		&channel.User2ID,
		&channel.CreatedAt,
		&channel.UpdatedAt,
	)

	if err == nil {
		return channel, nil
	}

	if err != pgx.ErrNoRows {
		return nil, err
	}

	insertQuery := `
		INSERT INTO dm_channels (id, user1_id, user2_id)
		VALUES ($1, $2, $3)
		RETURNING id, user1_id, user2_id, created_at, updated_at
	`

	channel.ID = uuid.New()
	err = r.pool.QueryRow(ctx, insertQuery, channel.ID, user1ID, user2ID).Scan(
		&channel.ID,
		&channel.User1ID,
		&channel.User2ID,
		&channel.CreatedAt,
		&channel.UpdatedAt,
	)

	return channel, err
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*DMChannel, error) {
	query := `
		SELECT id, user1_id, user2_id, created_at, updated_at
		FROM dm_channels
		WHERE id = $1
	`

	channel := &DMChannel{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&channel.ID,
		&channel.User1ID,
		&channel.User2ID,
		&channel.CreatedAt,
		&channel.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("DM channel not found")
	}

	return channel, err
}

func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID) ([]*DMChannelWithUser, error) {
	query := `
		SELECT 
			dc.id, dc.user1_id, dc.user2_id, dc.created_at, dc.updated_at,
			u.id as other_user_id, u.handle, u.display_name, u.avatar_url, u.status
		FROM dm_channels dc
		JOIN users u ON (
			CASE 
				WHEN dc.user1_id = $1 THEN dc.user2_id = u.id
				ELSE dc.user1_id = u.id
			END
		)
		WHERE dc.user1_id = $1 OR dc.user2_id = $1
		ORDER BY dc.updated_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []*DMChannelWithUser
	for rows.Next() {
		ch := &DMChannelWithUser{Channel: &DMChannel{}}
		err := rows.Scan(
			&ch.Channel.ID,
			&ch.Channel.User1ID,
			&ch.Channel.User2ID,
			&ch.Channel.CreatedAt,
			&ch.Channel.UpdatedAt,
			&ch.OtherUserID,
			&ch.OtherUserHandle,
			&ch.OtherUserDisplay,
			&ch.OtherUserAvatar,
			&ch.OtherUserStatus,
		)
		if err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}

	return channels, rows.Err()
}

func (r *Repository) UpdateTimestamp(ctx context.Context, id uuid.UUID) error {
	query := `UPDATE dm_channels SET updated_at = NOW() WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, id)
	return err
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM dm_channels WHERE id = $1`
	result, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("DM channel not found")
	}
	return nil
}

func (r *Repository) IsParticipant(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1 FROM dm_channels
			WHERE id = $1 AND (user1_id = $2 OR user2_id = $2)
		)
	`

	var exists bool
	err := r.pool.QueryRow(ctx, query, channelID, userID).Scan(&exists)
	return exists, err
}
