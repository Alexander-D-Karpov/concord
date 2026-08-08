package message

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/messaging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AddReaction adds a (user, emoji) reaction to a message and returns the created
// Reaction. The insert is idempotent per (message, user, emoji): a duplicate hits
// the ON CONFLICT DO NOTHING and returns a Conflict error rather than creating a
// second row.
func (c *Core) AddReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (*messaging.Reaction, error) {
	r := &messaging.Reaction{ID: uuid.New(), MessageID: messageID, UserID: userID, Emoji: emoji, CreatedAt: time.Now()}
	err := c.pool.QueryRow(ctx,
		fmt.Sprintf(`INSERT INTO %s (id, message_id, user_id, emoji, created_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (message_id, user_id, emoji) DO NOTHING
			RETURNING created_at`, c.spec.Reactions),
		r.ID, messageID, userID, emoji, r.CreatedAt,
	).Scan(&r.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, errors.Conflict("reaction already exists")
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// RemoveReaction deletes a user's reaction and returns the deleted reaction's ID,
// or a NotFound error (with uuid.Nil) if no matching reaction existed.
func (c *Core) RemoveReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (uuid.UUID, error) {
	var id uuid.UUID
	err := c.pool.QueryRow(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE message_id = $1 AND user_id = $2 AND emoji = $3 RETURNING id`, c.spec.Reactions),
		messageID, userID, emoji,
	).Scan(&id)
	if err == pgx.ErrNoRows {
		return uuid.Nil, errors.NotFound("reaction not found")
	}
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
