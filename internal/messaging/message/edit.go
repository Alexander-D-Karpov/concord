package message

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/messaging"
	"github.com/jackc/pgx/v5"
)

// Edit updates a message's content in a transaction: it locks the row FOR UPDATE,
// records the prior content via the spec's RecordEdit seam, then writes the new
// content, bumps edit_count, and stamps edited_at (also set on m). It returns
// NotFound if the message is missing or soft-deleted, and sets m.EditCount from
// the returned count.
func (c *Core) Edit(ctx context.Context, m *messaging.Message) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var prev string
	err = tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT content FROM %s WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, c.spec.Messages),
		m.ID,
	).Scan(&prev)
	if err == pgx.ErrNoRows {
		return errors.NotFound("message not found")
	}
	if err != nil {
		return err
	}

	if err := c.spec.RecordEdit(ctx, tx, m.ID, prev); err != nil {
		return err
	}

	now := time.Now()
	m.EditedAt = &now
	err = tx.QueryRow(ctx,
		fmt.Sprintf(`UPDATE %s SET content = $2, edited_at = $3, edit_count = COALESCE(edit_count, 0) + 1
			WHERE id = $1 AND deleted_at IS NULL RETURNING edit_count`, c.spec.Messages),
		m.ID, m.Content, m.EditedAt,
	).Scan(&m.EditCount)
	if err == pgx.ErrNoRows {
		return errors.NotFound("message not found")
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SoftDelete marks a message deleted by stamping deleted_at, leaving the row in
// place. It returns NotFound if the message does not exist or was already deleted.
func (c *Core) SoftDelete(ctx context.Context, id int64) error {
	res, err := c.pool.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL`, c.spec.Messages),
		id, time.Now())
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return errors.NotFound("message not found")
	}
	return nil
}
