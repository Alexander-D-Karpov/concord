package message

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/messaging"
	"github.com/google/uuid"
)

// List returns a page of non-deleted messages for a scope using keyset
// pagination on message ID. With beforeID it returns older messages, with afterID
// newer ones, and with neither the most recent messages. Results are always
// ordered oldest-first: the default and beforeID branches query DESC and are
// reversed in place, while afterID is already ASC.
func (c *Core) List(ctx context.Context, scopeID uuid.UUID, beforeID, afterID *int64, limit int, loadMentions bool) ([]*messaging.Message, error) {
	cols := c.spec.selectColumns()
	scope := c.spec.ScopeColumn

	var q string
	var args []any
	switch {
	case beforeID != nil:
		q = fmt.Sprintf(`SELECT %s FROM %s m WHERE m.%s = $1 AND m.id < $2 AND m.deleted_at IS NULL ORDER BY m.id DESC LIMIT $3`, cols, c.spec.Messages, scope)
		args = []any{scopeID, *beforeID, limit}
	case afterID != nil:
		q = fmt.Sprintf(`SELECT %s FROM %s m WHERE m.%s = $1 AND m.id > $2 AND m.deleted_at IS NULL ORDER BY m.id ASC LIMIT $3`, cols, c.spec.Messages, scope)
		args = []any{scopeID, *afterID, limit}
	default:
		q = fmt.Sprintf(`SELECT %s FROM %s m WHERE m.%s = $1 AND m.deleted_at IS NULL ORDER BY m.id DESC LIMIT $2`, cols, c.spec.Messages, scope)
		args = []any{scopeID, limit}
	}

	msgs, err := c.QueryAndLoad(ctx, q, loadMentions, args...)
	if err != nil {
		return nil, err
	}

	if beforeID == nil && afterID == nil {
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	}
	return msgs, nil
}
