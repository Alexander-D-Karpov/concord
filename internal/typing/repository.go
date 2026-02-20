package typing

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

type TypingIndicator struct {
	UserID    uuid.UUID
	RoomID    *uuid.UUID
	ChannelID *uuid.UUID
	StartedAt time.Time
	ExpiresAt time.Time
}

const TypingDuration = 5 * time.Second

func (r *Repository) SetTypingInRoom(ctx context.Context, userID, roomID uuid.UUID) error {
	expiresAt := time.Now().Add(TypingDuration)

	query := `
		INSERT INTO typing_indicators (user_id, room_id, channel_id, started_at, expires_at)
		VALUES ($1, $2, NULL, NOW(), $3)
		ON CONFLICT (user_id, room_id) WHERE room_id IS NOT NULL
		DO UPDATE SET started_at = NOW(), expires_at = $3
	`

	_, err := r.pool.Exec(ctx, query, userID, roomID, expiresAt)
	return err
}

func (r *Repository) SetTypingInDM(ctx context.Context, userID, channelID uuid.UUID) error {
	expiresAt := time.Now().Add(TypingDuration)

	query := `
		INSERT INTO typing_indicators (user_id, room_id, channel_id, started_at, expires_at)
		VALUES ($1, NULL, $2, NOW(), $3)
		ON CONFLICT (user_id, channel_id) WHERE channel_id IS NOT NULL
		DO UPDATE SET started_at = NOW(), expires_at = $3
	`

	_, err := r.pool.Exec(ctx, query, userID, channelID, expiresAt)
	return err
}

func (r *Repository) ClearTypingInRoom(ctx context.Context, userID, roomID uuid.UUID) error {
	query := `DELETE FROM typing_indicators WHERE user_id = $1 AND room_id = $2`
	_, err := r.pool.Exec(ctx, query, userID, roomID)
	return err
}

func (r *Repository) ClearTypingInDM(ctx context.Context, userID, channelID uuid.UUID) error {
	query := `DELETE FROM typing_indicators WHERE user_id = $1 AND channel_id = $2`
	_, err := r.pool.Exec(ctx, query, userID, channelID)
	return err
}

func (r *Repository) GetTypingInRoom(ctx context.Context, roomID uuid.UUID) ([]TypingIndicator, error) {
	query := `
		SELECT user_id, room_id, channel_id, started_at, expires_at
		FROM typing_indicators
		WHERE room_id = $1 AND expires_at > NOW()
	`

	rows, err := r.pool.Query(ctx, query, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indicators []TypingIndicator
	for rows.Next() {
		var ind TypingIndicator
		if err := rows.Scan(&ind.UserID, &ind.RoomID, &ind.ChannelID, &ind.StartedAt, &ind.ExpiresAt); err != nil {
			return nil, err
		}
		indicators = append(indicators, ind)
	}

	return indicators, rows.Err()
}

func (r *Repository) GetTypingInDM(ctx context.Context, channelID uuid.UUID) ([]TypingIndicator, error) {
	query := `
		SELECT user_id, room_id, channel_id, started_at, expires_at
		FROM typing_indicators
		WHERE channel_id = $1 AND expires_at > NOW()
	`

	rows, err := r.pool.Query(ctx, query, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indicators []TypingIndicator
	for rows.Next() {
		var ind TypingIndicator
		if err := rows.Scan(&ind.UserID, &ind.RoomID, &ind.ChannelID, &ind.StartedAt, &ind.ExpiresAt); err != nil {
			return nil, err
		}
		indicators = append(indicators, ind)
	}

	return indicators, rows.Err()
}

func (r *Repository) CleanupExpired(ctx context.Context) (int64, error) {
	result, err := r.pool.Exec(ctx, `DELETE FROM typing_indicators WHERE expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (r *Repository) GetAndDeleteExpired(ctx context.Context) ([]TypingIndicator, error) {
	query := `
		DELETE FROM typing_indicators 
		WHERE expires_at < NOW()
		RETURNING user_id, room_id, channel_id, started_at, expires_at
	`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indicators []TypingIndicator
	for rows.Next() {
		var ind TypingIndicator
		if err := rows.Scan(&ind.UserID, &ind.RoomID, &ind.ChannelID, &ind.StartedAt, &ind.ExpiresAt); err != nil {
			return nil, err
		}
		indicators = append(indicators, ind)
	}

	return indicators, rows.Err()
}
