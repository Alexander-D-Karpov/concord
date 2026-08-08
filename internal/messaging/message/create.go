package message

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/messaging"
	"github.com/google/uuid"
)

// Create inserts a message and its attachments in a single transaction, also
// invoking the spec's MediaInsert seam per attachment so media records stay in
// sync. It rejects the message with a BadRequest when MaxAttachments is exceeded.
// The caller must have set m.ID; attachment IDs and timestamps are filled in as a
// side effect. Any error rolls the whole insert back.
func (c *Core) Create(ctx context.Context, m *messaging.Message) error {
	if c.spec.MaxAttachments > 0 && len(m.Attachments) > c.spec.MaxAttachments {
		return errors.BadRequest("too many attachments")
	}

	scopeID := m.SurfaceID()
	var fwdScope *uuid.UUID
	switch c.spec.Surface {
	case messaging.SurfaceRoom:
		fwdScope = m.ForwardFromRoomID
	case messaging.SurfaceDM:
		fwdScope = m.ForwardFromChannelID
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	insertQ := fmt.Sprintf(`INSERT INTO %s (
		id, %s, author_id, content, created_at, reply_to_id,
		forwarded_from_user_id, forwarded_from_user_name, %s,
		forwarded_from_message_id, forwarded_original_timestamp,
		media_group_id, reply_quoted_content, reply_mention_author
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		c.spec.Messages, c.spec.ScopeColumn, c.spec.ForwardScopeColumn)

	if _, err := tx.Exec(ctx, insertQ,
		m.ID, scopeID, m.AuthorID, m.Content, m.CreatedAt, m.ReplyToID,
		m.ForwardFromUserID, m.ForwardFromUserName, fwdScope,
		m.ForwardFromMsgID, m.ForwardOriginalTS,
		m.MediaGroupID, m.ReplyQuotedContent, m.ReplyMentionAuthor,
	); err != nil {
		return err
	}

	attQ := fmt.Sprintf(`INSERT INTO %s (id, message_id, url, filename, content_type, size, width, height, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, c.spec.Attachments)
	for i := range m.Attachments {
		att := &m.Attachments[i]
		if att.ID == uuid.Nil {
			att.ID = uuid.New()
		}
		att.MessageID = m.ID
		att.CreatedAt = m.CreatedAt
		if _, err := tx.Exec(ctx, attQ,
			att.ID, att.MessageID, att.URL, att.Filename, att.ContentType, att.Size, att.Width, att.Height, att.CreatedAt,
		); err != nil {
			return err
		}
		if err := c.spec.MediaInsert(ctx, tx, m.ID, scopeID, att.URL, att.ContentType, att.Width, att.Height, att.Size, att.CreatedAt); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}
