package editing

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Recorder appends message edit history. It is stateless and holds no pool; every
// write takes a caller-supplied pgx.Tx so history is committed atomically with the
// message update.
type Recorder struct{}

// NewRecorder returns a Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// RecordRoom appends the previous content of a room message to message_edits with
// version MAX(version)+1. It must run inside the same tx that updates the message;
// the version subquery can race under concurrent edits without row locking.
func (r *Recorder) RecordRoom(ctx context.Context, tx pgx.Tx, messageID int64, previousContent string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO message_edits (message_id, previous_content, version)
		 VALUES ($1, $2, COALESCE((SELECT MAX(version) FROM message_edits WHERE message_id = $1), 0) + 1)`,
		messageID, previousContent)
	return err
}

// RecordDM is the DM counterpart of RecordRoom, writing to dm_message_edits. The
// same tx and version-race caveats apply.
func (r *Recorder) RecordDM(ctx context.Context, tx pgx.Tx, messageID int64, previousContent string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO dm_message_edits (message_id, previous_content, version)
		 VALUES ($1, $2, COALESCE((SELECT MAX(version) FROM dm_message_edits WHERE message_id = $1), 0) + 1)`,
		messageID, previousContent)
	return err
}
