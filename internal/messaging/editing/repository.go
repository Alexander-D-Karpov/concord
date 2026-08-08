package editing

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Entry is one recorded edit-history revision: the content the message held
// before that edit, when the edit happened, and its monotonically increasing
// Version.
type Entry struct {
	ID              string
	PreviousContent string
	EditedAt        time.Time
	Version         int
}

// Reader reads message edit history from the pool. It is the read counterpart of
// Recorder.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader returns a Reader backed by pool.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

// ListRoom returns a room message's edit history from message_edits, newest
// version first.
func (r *Reader) ListRoom(ctx context.Context, messageID int64) ([]Entry, error) {
	return r.list(ctx, `SELECT id, previous_content, edited_at, version FROM message_edits WHERE message_id = $1 ORDER BY version DESC`, messageID)
}

// ListDM returns a DM message's edit history from dm_message_edits, newest
// version first.
func (r *Reader) ListDM(ctx context.Context, messageID int64) ([]Entry, error) {
	return r.list(ctx, `SELECT id, previous_content, edited_at, version FROM dm_message_edits WHERE message_id = $1 ORDER BY version DESC`, messageID)
}

// list runs the shared edit-history query for either surface. Rows that fail to
// scan are skipped rather than aborting the whole read.
func (r *Reader) list(ctx context.Context, sql string, messageID int64) ([]Entry, error) {
	rows, err := r.pool.Query(ctx, sql, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.PreviousContent, &e.EditedAt, &e.Version); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Keep the pgx import referenced even if pgx.ErrNoRows is only used elsewhere.
var _ = pgx.ErrNoRows
