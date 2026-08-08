package message

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/messaging"
	"github.com/google/uuid"
)

// Pin marks a message pinned within its surface scope, recording who pinned it.
// It is idempotent: re-pinning an already-pinned message is a no-op via ON
// CONFLICT DO NOTHING and returns no error.
func (c *Core) Pin(ctx context.Context, scopeID uuid.UUID, messageID int64, pinnedBy uuid.UUID) error {
	_, err := c.pool.Exec(ctx,
		fmt.Sprintf(`INSERT INTO %s (%s, message_id, pinned_by) VALUES ($1,$2,$3)
			ON CONFLICT (%s, message_id) DO NOTHING`, c.spec.Pinned, c.spec.PinnedFK, c.spec.PinnedFK),
		scopeID, messageID, pinnedBy)
	return err
}

// Unpin removes a message's pin within its scope, returning NotFound if the
// message was not pinned.
func (c *Core) Unpin(ctx context.Context, scopeID uuid.UUID, messageID int64) error {
	res, err := c.pool.Exec(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE %s = $1 AND message_id = $2`, c.spec.Pinned, c.spec.PinnedFK),
		scopeID, messageID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return errors.NotFound("pinned message not found")
	}
	return nil
}

// ListPinned returns the non-deleted pinned messages for a scope, ordered by pin
// time newest-first, fully hydrated (mentions only when loadMentions is set).
func (c *Core) ListPinned(ctx context.Context, scopeID uuid.UUID, loadMentions bool) ([]*messaging.Message, error) {
	q := fmt.Sprintf(`SELECT %s FROM %s m
		INNER JOIN %s pm ON m.id = pm.message_id
		WHERE pm.%s = $1 AND m.deleted_at IS NULL
		ORDER BY pm.pinned_at DESC`,
		c.spec.selectColumns(), c.spec.Messages, c.spec.Pinned, c.spec.PinnedFK)
	return c.QueryAndLoad(ctx, q, loadMentions, scopeID)
}
