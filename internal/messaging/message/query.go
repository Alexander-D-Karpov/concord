package message

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/messaging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// GetByID loads a single fully-hydrated message (core fields plus reactions,
// attachments, and mentions). It returns a NotFound error if no row matches; note
// it does not filter deleted_at, so soft-deleted messages are still returned.
func (c *Core) GetByID(ctx context.Context, id int64) (*messaging.Message, error) {
	q := fmt.Sprintf(`SELECT %s FROM %s m WHERE m.id = $1`, c.spec.selectColumns(), c.spec.Messages)
	m := &messaging.Message{}
	err := c.spec.scanCore(c.pool.QueryRow(ctx, q, id), m)
	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("message not found")
	}
	if err != nil {
		return nil, err
	}

	ids := []int64{id}
	ra, err := c.GetReactionsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	aa, err := c.GetAttachmentsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	ma, err := c.GetMentionsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	m.Reactions = ra[id]
	m.Attachments = aa[id]
	m.Mentions = ma[id]
	return m, nil
}

// QueryAndLoad runs an arbitrary selectColumns-shaped query, scans all rows into
// messages, then batch-loads reactions and attachments (and mentions only when
// loadMentions is set, since it costs an extra query) for the whole result set.
// It returns messages in the query's row order.
func (c *Core) QueryAndLoad(ctx context.Context, query string, loadMentions bool, args ...any) ([]*messaging.Message, error) {
	rows, err := c.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*messaging.Message
	var ids []int64
	for rows.Next() {
		m := &messaging.Message{}
		if err := c.spec.scanCore(rows, m); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
		ids = append(ids, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return msgs, nil
	}

	ra, err := c.GetReactionsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	aa, err := c.GetAttachmentsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	var ma map[int64][]uuid.UUID
	if loadMentions {
		ma, err = c.GetMentionsBatch(ctx, ids)
		if err != nil {
			return nil, err
		}
	}
	for _, m := range msgs {
		m.Reactions = ra[m.ID]
		m.Attachments = aa[m.ID]
		if loadMentions {
			m.Mentions = ma[m.ID]
		}
	}
	return msgs, nil
}
