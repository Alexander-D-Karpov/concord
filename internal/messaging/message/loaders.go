package message

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/messaging"
	"github.com/google/uuid"
)

// GetReactionsBatch loads reactions for many messages at once, returned keyed by
// message ID with each slice ordered oldest-first. An empty ids slice yields an
// empty (non-nil) map without querying; messages with no reactions are simply
// absent from the map.
func (c *Core) GetReactionsBatch(ctx context.Context, ids []int64) (map[int64][]messaging.Reaction, error) {
	if len(ids) == 0 {
		return map[int64][]messaging.Reaction{}, nil
	}
	q := fmt.Sprintf(`SELECT id, message_id, user_id, emoji, created_at
		FROM %s WHERE message_id = ANY($1) ORDER BY created_at ASC`, c.spec.Reactions)
	rows, err := c.pool.Query(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64][]messaging.Reaction)
	for rows.Next() {
		var r messaging.Reaction
		if err := rows.Scan(&r.ID, &r.MessageID, &r.UserID, &r.Emoji, &r.CreatedAt); err != nil {
			return nil, err
		}
		out[r.MessageID] = append(out[r.MessageID], r)
	}
	return out, rows.Err()
}

// GetAttachmentsBatch loads attachments for many messages at once, returned keyed
// by message ID with each slice ordered oldest-first. An empty ids slice yields an
// empty (non-nil) map without querying.
func (c *Core) GetAttachmentsBatch(ctx context.Context, ids []int64) (map[int64][]messaging.Attachment, error) {
	if len(ids) == 0 {
		return map[int64][]messaging.Attachment{}, nil
	}
	q := fmt.Sprintf(`SELECT id, message_id, url, filename, content_type, size, width, height, created_at
		FROM %s WHERE message_id = ANY($1) ORDER BY created_at ASC`, c.spec.Attachments)
	rows, err := c.pool.Query(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64][]messaging.Attachment)
	for rows.Next() {
		var a messaging.Attachment
		if err := rows.Scan(&a.ID, &a.MessageID, &a.URL, &a.Filename, &a.ContentType, &a.Size, &a.Width, &a.Height, &a.CreatedAt); err != nil {
			return nil, err
		}
		out[a.MessageID] = append(out[a.MessageID], a)
	}
	return out, rows.Err()
}

// GetMentionsBatch loads mentioned user IDs for many messages at once, returned
// keyed by message ID. An empty ids slice yields an empty (non-nil) map without
// querying.
func (c *Core) GetMentionsBatch(ctx context.Context, ids []int64) (map[int64][]uuid.UUID, error) {
	if len(ids) == 0 {
		return map[int64][]uuid.UUID{}, nil
	}
	q := fmt.Sprintf(`SELECT message_id, user_id FROM %s WHERE message_id = ANY($1)`, c.spec.Mentions)
	rows, err := c.pool.Query(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64][]uuid.UUID)
	for rows.Next() {
		var mid int64
		var uid uuid.UUID
		if err := rows.Scan(&mid, &uid); err != nil {
			return nil, err
		}
		out[mid] = append(out[mid], uid)
	}
	return out, rows.Err()
}
