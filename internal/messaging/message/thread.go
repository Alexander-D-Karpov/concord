package message

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/messaging"
)

// Thread returns the non-deleted replies to parentID (messages whose reply_to_id
// matches), ordered oldest-first, paged by limit/offset and fully hydrated
// (mentions only when loadMentions is set).
func (c *Core) Thread(ctx context.Context, parentID int64, limit, offset int, loadMentions bool) ([]*messaging.Message, error) {
	q := fmt.Sprintf(`SELECT %s FROM %s m
		WHERE m.reply_to_id = $1 AND m.deleted_at IS NULL
		ORDER BY m.id ASC LIMIT $2 OFFSET $3`, c.spec.selectColumns(), c.spec.Messages)
	return c.QueryAndLoad(ctx, q, loadMentions, parentID, limit, offset)
}
