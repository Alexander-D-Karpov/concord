package typing

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository persists typing indicators in Postgres.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository over the given connection pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// TypingIndicator is one persisted "user is typing" row. Exactly one of RoomID or
// ChannelID is set (the other is nil), distinguishing room vs DM typing.
type TypingIndicator struct {
	UserID    uuid.UUID
	RoomID    *uuid.UUID
	ChannelID *uuid.UUID
	StartedAt time.Time
	ExpiresAt time.Time
}

// TypingDuration is how long a typing indicator stays live before it expires and
// becomes eligible for reaping.
const TypingDuration = 5 * time.Second

// SetTypingInRoom upserts the caller's typing indicator for a room, refreshing
// started_at/expires_at (now + TypingDuration) on conflict so repeated keystrokes
// extend the indicator.
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

// SetTypingInDM upserts the caller's typing indicator for a DM channel,
// refreshing expiry on conflict just like SetTypingInRoom.
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

// ClearTypingInRoom deletes the caller's room typing indicator (e.g. on send or
// explicit stop); deleting a nonexistent row is a no-op.
func (r *Repository) ClearTypingInRoom(ctx context.Context, userID, roomID uuid.UUID) error {
	query := `DELETE FROM typing_indicators WHERE user_id = $1 AND room_id = $2`
	_, err := r.pool.Exec(ctx, query, userID, roomID)
	return err
}

// ClearTypingInDM deletes the caller's DM typing indicator; a no-op if none
// exists.
func (r *Repository) ClearTypingInDM(ctx context.Context, userID, channelID uuid.UUID) error {
	query := `DELETE FROM typing_indicators WHERE user_id = $1 AND channel_id = $2`
	_, err := r.pool.Exec(ctx, query, userID, channelID)
	return err
}

// GetTypingInRoom returns the currently active (not yet expired) typing
// indicators for a room; expired rows are filtered out by the query.
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

// GetTypingInDM returns the currently active typing indicators for a DM channel,
// excluding expired rows.
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

// CleanupExpired deletes all expired typing indicators and returns how many rows
// were removed. Unlike GetAndDeleteExpired it does not return the deleted rows, so
// no "stopped" events can be broadcast from its result.
func (r *Repository) CleanupExpired(ctx context.Context) (int64, error) {
	result, err := r.pool.Exec(ctx, `DELETE FROM typing_indicators WHERE expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// GetAndDeleteExpired atomically deletes expired typing indicators and returns the
// removed rows (via RETURNING), so the service can broadcast a stop event for each.
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
