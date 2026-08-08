package features

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository is the data-access layer for the features service (forwards,
// scheduled messages, bookmarks, drafts, notification overrides, media, stickers,
// and polls) over a single Postgres pool.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository over the given connection pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ForwardSource is the subset of an original message needed to build a forwarded
// copy.
type ForwardSource struct {
	AuthorID  uuid.UUID
	Content   string
	CreatedAt time.Time
}

// GetRoomMessage loads the forward source for a room message, ignoring
// soft-deleted rows (returns an error, e.g. pgx.ErrNoRows, if not found).
func (r *Repository) GetRoomMessage(ctx context.Context, msgID int64, roomID uuid.UUID) (*ForwardSource, error) {
	var fs ForwardSource
	err := r.pool.QueryRow(ctx,
		`SELECT author_id, content, created_at FROM messages WHERE id = $1 AND room_id = $2 AND deleted_at IS NULL`,
		msgID, roomID).Scan(&fs.AuthorID, &fs.Content, &fs.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &fs, nil
}

// GetDMMessage loads the forward source for a DM message, ignoring soft-deleted
// rows.
func (r *Repository) GetDMMessage(ctx context.Context, msgID int64, channelID uuid.UUID) (*ForwardSource, error) {
	var fs ForwardSource
	err := r.pool.QueryRow(ctx,
		`SELECT author_id, content, created_at FROM dm_messages WHERE id = $1 AND channel_id = $2 AND deleted_at IS NULL`,
		msgID, channelID).Scan(&fs.AuthorID, &fs.Content, &fs.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &fs, nil
}

// GetUserDisplayName returns a user's display name, or "" if the user is missing
// or the query fails (the error is swallowed).
func (r *Repository) GetUserDisplayName(ctx context.Context, userID uuid.UUID) string {
	var name string
	_ = r.pool.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, userID).Scan(&name)
	return name
}

// InsertForwardedRoomMessage inserts a forwarded message into a room, preserving
// the original author/room/message/timestamp as forwarded_from_* attribution.
// The fwd* pointers may be nil when the caller drops author attribution.
func (r *Repository) InsertForwardedRoomMessage(ctx context.Context, id int64, roomID, authorID uuid.UUID, content string, createdAt time.Time, fwdUserID *uuid.UUID, fwdUserName *string, fwdRoomID *uuid.UUID, fwdMsgID int64, fwdTimestamp time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO messages (id, room_id, author_id, content, created_at,
		 forwarded_from_user_id, forwarded_from_user_name, forwarded_from_room_id,
		 forwarded_from_message_id, forwarded_original_timestamp)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		id, roomID, authorID, content, createdAt,
		fwdUserID, fwdUserName, fwdRoomID, fwdMsgID, fwdTimestamp)
	return err
}

// InsertForwardedDMMessage inserts a forwarded message into a DM channel with
// forwarded_from_* attribution, mirroring InsertForwardedRoomMessage.
func (r *Repository) InsertForwardedDMMessage(ctx context.Context, id int64, channelID, authorID uuid.UUID, content string, createdAt time.Time, fwdUserID *uuid.UUID, fwdUserName *string, fwdChannelID *uuid.UUID, fwdMsgID int64, fwdTimestamp time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO dm_messages (id, channel_id, author_id, content, created_at,
		 forwarded_from_user_id, forwarded_from_user_name, forwarded_from_channel_id,
		 forwarded_from_message_id, forwarded_original_timestamp)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		id, channelID, authorID, content, createdAt,
		fwdUserID, fwdUserName, fwdChannelID, fwdMsgID, fwdTimestamp)
	return err
}

// ScheduledMessageRow is a row of the scheduled_messages table. Exactly one of
// RoomID or ChannelID identifies the destination; AttemptCount tracks delivery
// retries.
type ScheduledMessageRow struct {
	ID           int64
	RoomID       *uuid.UUID
	ChannelID    *uuid.UUID
	AuthorID     uuid.UUID
	Content      string
	ReplyToID    *int64
	ScheduledFor time.Time
	Status       string
	CreatedAt    time.Time
	AttemptCount int
}

// InsertScheduledMessage creates a pending scheduled message and returns its
// generated id and created_at.
func (r *Repository) InsertScheduledMessage(ctx context.Context, roomID, channelID *uuid.UUID, authorID uuid.UUID, content string, replyToID *int64, scheduledFor time.Time) (int64, time.Time, error) {
	var id int64
	var createdAt time.Time
	err := r.pool.QueryRow(ctx,
		`INSERT INTO scheduled_messages (room_id, channel_id, author_id, content, reply_to_id, scheduled_for)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 RETURNING id, created_at`,
		roomID, channelID, authorID, content, replyToID, scheduledFor,
	).Scan(&id, &createdAt)
	return id, createdAt, err
}

// ListScheduledMessages returns the author's pending scheduled messages, optionally
// filtered to a room or channel (room takes precedence if both are set), ordered by
// scheduled_for ascending. Rows that fail to scan are skipped rather than aborting.
func (r *Repository) ListScheduledMessages(ctx context.Context, authorID uuid.UUID, roomID, channelID *uuid.UUID) ([]ScheduledMessageRow, error) {
	query := `SELECT id, room_id, channel_id, content, reply_to_id, scheduled_for, status, created_at
			  FROM scheduled_messages WHERE author_id = $1 AND status = 'pending'`
	args := []interface{}{authorID}

	if roomID != nil {
		query += " AND room_id = $2"
		args = append(args, *roomID)
	} else if channelID != nil {
		query += " AND channel_id = $2"
		args = append(args, *channelID)
	}

	query += " ORDER BY scheduled_for ASC"

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ScheduledMessageRow
	for rows.Next() {
		var row ScheduledMessageRow
		if err := rows.Scan(&row.ID, &row.RoomID, &row.ChannelID, &row.Content, &row.ReplyToID, &row.ScheduledFor, &row.Status, &row.CreatedAt); err != nil {
			continue
		}
		row.AuthorID = authorID
		result = append(result, row)
	}
	return result, nil
}

// UpdateScheduledMessage edits the content and scheduled time of a pending message
// owned by authorID; it affects no rows (and returns nil) if the message is not
// pending or not owned by the author.
func (r *Repository) UpdateScheduledMessage(ctx context.Context, id int64, authorID string, content string, scheduledFor time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduled_messages SET content = $2, scheduled_for = $3, updated_at = NOW()
		 WHERE id = $1 AND author_id = $4 AND status = 'pending'`,
		id, content, scheduledFor, authorID)
	return err
}

// CancelScheduledMessage marks a pending message owned by authorID as 'cancelled';
// a no-op if it is not pending or not owned by the author.
func (r *Repository) CancelScheduledMessage(ctx context.Context, id int64, authorID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduled_messages SET status = 'cancelled', updated_at = NOW()
		 WHERE id = $1 AND author_id = $2 AND status = 'pending'`,
		id, authorID)
	return err
}

// ClaimNextScheduledMessage atomically claims the oldest due pending message for
// delivery: it selects with FOR UPDATE SKIP LOCKED (so concurrent workers never
// grab the same row), flips status to 'processing', stamps processing_started_at,
// and increments attempt_count. Returns (nil, nil) when nothing is due.
func (r *Repository) ClaimNextScheduledMessage(ctx context.Context) (*ScheduledMessageRow, error) {
	var row ScheduledMessageRow
	err := r.pool.QueryRow(ctx, `
		WITH next_job AS (
			SELECT id
			FROM scheduled_messages
			WHERE status = 'pending' AND scheduled_for <= NOW()
			ORDER BY scheduled_for
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE scheduled_messages sm
		SET status = 'processing', processing_started_at = NOW(),
		    attempt_count = sm.attempt_count + 1, updated_at = NOW()
		FROM next_job
		WHERE sm.id = next_job.id
		RETURNING sm.id, sm.room_id, sm.channel_id, sm.author_id, sm.content, sm.reply_to_id, sm.attempt_count
	`).Scan(&row.ID, &row.RoomID, &row.ChannelID, &row.AuthorID, &row.Content, &row.ReplyToID, &row.AttemptCount)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// MarkScheduledSent marks a claimed message as 'sent' and clears
// processing_started_at, completing its lifecycle.
func (r *Repository) MarkScheduledSent(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduled_messages SET status = 'sent', processing_started_at = NULL, updated_at = NOW() WHERE id = $1`, id)
	return err
}

// MarkScheduledFailed records a delivery failure. When retry is true the row goes
// back to 'pending' for another attempt unless attempt_count has reached
// maxAttempts, in which case it becomes 'failed'; when retry is false it is marked
// 'failed' outright. It always stores lastErr and clears processing_started_at.
func (r *Repository) MarkScheduledFailed(ctx context.Context, id int64, lastErr string, retry bool, maxAttempts int) error {
	if retry {
		_, err := r.pool.Exec(ctx, `
			UPDATE scheduled_messages
			SET status = CASE WHEN attempt_count >= $3 THEN 'failed' ELSE 'pending' END,
			    last_error = $2, processing_started_at = NULL, updated_at = NOW()
			WHERE id = $1`, id, lastErr, maxAttempts)
		return err
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduled_messages SET status = 'failed', last_error = $2, processing_started_at = NULL, updated_at = NOW() WHERE id = $1`,
		id, lastErr)
	return err
}

// RecoverStuckScheduledMessages resets messages that have been stuck in
// 'processing' longer than olderThan (e.g. a worker crashed mid-delivery) back to
// 'pending' so they get retried, and returns how many were recovered.
func (r *Repository) RecoverStuckScheduledMessages(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE scheduled_messages
		SET status = 'pending', processing_started_at = NULL, updated_at = NOW()
		WHERE status = 'processing' AND processing_started_at < NOW() - $1::interval`,
		olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// InsertRoomMessage inserts a delivered scheduled message into a room, using ON
// CONFLICT (id) DO NOTHING so a retry after a partial delivery is idempotent.
func (r *Repository) InsertRoomMessage(ctx context.Context, id int64, roomID, authorID uuid.UUID, content string, createdAt time.Time, replyToID *int64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO messages (id, room_id, author_id, content, created_at, reply_to_id)
		 VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`,
		id, roomID, authorID, content, createdAt, replyToID)
	return err
}

// InsertDMMessage inserts a delivered scheduled message into a DM channel,
// idempotent via ON CONFLICT (id) DO NOTHING.
func (r *Repository) InsertDMMessage(ctx context.Context, id int64, channelID, authorID uuid.UUID, content string, createdAt time.Time, replyToID *int64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO dm_messages (id, channel_id, author_id, content, created_at, reply_to_id)
		 VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`,
		id, channelID, authorID, content, createdAt, replyToID)
	return err
}

// GetOrSetScheduledMessageID assigns newMsgID as the scheduled row's
// sent_message_id only if none is set yet (COALESCE), and returns the effective id.
// This makes delivery idempotent: a retry reuses the id chosen on the first
// attempt instead of allocating a duplicate message.
func (r *Repository) GetOrSetScheduledMessageID(ctx context.Context, scheduledID, newMsgID int64) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `
		UPDATE scheduled_messages
		SET sent_message_id = COALESCE(sent_message_id, $2)
		WHERE id = $1
		RETURNING sent_message_id`, scheduledID, newMsgID).Scan(&id)
	return id, err
}

// BookmarkRow is a saved-message bookmark; RoomID/ChannelID record where the
// bookmarked message lives.
type BookmarkRow struct {
	ID        uuid.UUID
	MessageID int64
	RoomID    *uuid.UUID
	ChannelID *uuid.UUID
	Note      string
	Tags      []string
	CreatedAt time.Time
}

// UpsertBookmark creates or updates the user's bookmark for a message (unique per
// user+message): on conflict it overwrites note and tags. Returns the bookmark id
// and created_at.
func (r *Repository) UpsertBookmark(ctx context.Context, userID uuid.UUID, messageID int64, roomID, channelID *uuid.UUID, note string, tags []string) (uuid.UUID, time.Time, error) {
	var id uuid.UUID
	var createdAt time.Time
	err := r.pool.QueryRow(ctx,
		`INSERT INTO bookmarks (user_id, message_id, room_id, channel_id, note, tags)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (user_id, message_id) DO UPDATE SET note = $5, tags = $6
		 RETURNING id, created_at`,
		userID, messageID, roomID, channelID, note, tags,
	).Scan(&id, &createdAt)
	return id, createdAt, err
}

// DeleteBookmark removes the user's bookmark for a message; a no-op if none exists.
func (r *Repository) DeleteBookmark(ctx context.Context, userID uuid.UUID, messageID int64) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM bookmarks WHERE user_id = $1 AND message_id = $2`, userID, messageID)
	return err
}

// ListBookmarks returns the user's bookmarks newest-first, up to limit. When tags
// is non-empty it keeps only bookmarks overlapping any of those tags (the &&
// array-overlap operator). Rows that fail to scan are skipped.
func (r *Repository) ListBookmarks(ctx context.Context, userID uuid.UUID, tags []string, limit int) ([]BookmarkRow, error) {
	query := `SELECT id, message_id, room_id, channel_id, note, tags, created_at
			  FROM bookmarks WHERE user_id = $1`
	args := []interface{}{userID}
	argIdx := 2

	if len(tags) > 0 {
		query += fmt.Sprintf(" AND tags && $%d", argIdx)
		args = append(args, tags)
		argIdx++
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []BookmarkRow
	for rows.Next() {
		var row BookmarkRow
		if err := rows.Scan(&row.ID, &row.MessageID, &row.RoomID, &row.ChannelID, &row.Note, &row.Tags, &row.CreatedAt); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

// EditHistoryRow is one prior version of an edited message; Version is the
// monotonically increasing revision number.
type EditHistoryRow struct {
	ID              uuid.UUID
	PreviousContent string
	EditedAt        time.Time
	Version         int
}

// InsertEditHistory appends a message's previous content as the next version
// (max(version)+1, starting at 1) so edits form an ordered history.
func (r *Repository) InsertEditHistory(ctx context.Context, messageID int64, content string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO message_edits (message_id, previous_content, version)
		 VALUES ($1, $2, COALESCE((SELECT MAX(version) FROM message_edits WHERE message_id = $1), 0) + 1)`,
		messageID, content)
	return err
}

// IncrementEditCount bumps the message's edit_count (treating NULL as 0).
func (r *Repository) IncrementEditCount(ctx context.Context, messageID int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE messages SET edit_count = COALESCE(edit_count, 0) + 1 WHERE id = $1`, messageID)
	return err
}

// ListEditHistory returns a message's edit history newest-version-first. Rows that
// fail to scan are skipped.
func (r *Repository) ListEditHistory(ctx context.Context, messageID int64) ([]EditHistoryRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, previous_content, edited_at, version FROM message_edits
		 WHERE message_id = $1 ORDER BY version DESC`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []EditHistoryRow
	for rows.Next() {
		var row EditHistoryRow
		if err := rows.Scan(&row.ID, &row.PreviousContent, &row.EditedAt, &row.Version); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

// PollRow is a poll's stored state. CorrectOption/Explanation are set only for
// quiz-type polls; CloseDate is nil for polls with no auto-close.
type PollRow struct {
	ID             uuid.UUID
	MessageID      int64
	RoomID         *uuid.UUID
	ChannelID      *uuid.UUID
	CreatorID      uuid.UUID
	Question       string
	PollType       int
	IsAnonymous    bool
	AllowsMultiple bool
	CorrectOption  *int
	Explanation    *string
	CloseDate      *time.Time
	IsClosed       bool
	TotalVoters    int
}

// PollOptionRow is one poll option with its cached vote tally.
type PollOptionRow struct {
	OptionID  int
	Text      string
	VoteCount int
}

// InsertPollTx inserts the poll header within tx (options are inserted separately
// via InsertPollOptionTx). closeDate may be nil for a poll that never auto-closes.
func (r *Repository) InsertPollTx(ctx context.Context, tx pgx.Tx, pollID uuid.UUID, msgID int64, roomID, channelID *uuid.UUID, creatorID uuid.UUID, question string, pollType int, isAnonymous, allowsMultiple bool, correctOption int, explanation string, closeDate *time.Time) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO polls (id, message_id, room_id, channel_id, creator_id, question, poll_type,
		 is_anonymous, allows_multiple, correct_option, explanation, close_date)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		pollID, msgID, roomID, channelID, creatorID, question, pollType,
		isAnonymous, allowsMultiple, correctOption, explanation, closeDate)
	return err
}

// InsertPollOptionTx inserts one poll option (identified by optionID) within tx.
func (r *Repository) InsertPollOptionTx(ctx context.Context, tx pgx.Tx, pollID uuid.UUID, optionID int, text string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO poll_options (poll_id, option_id, text) VALUES ($1,$2,$3)`,
		pollID, optionID, text)
	return err
}

// InsertRoomMessageTx inserts a room message within tx, used to create the
// carrier message for a poll atomically with the poll rows.
func (r *Repository) InsertRoomMessageTx(ctx context.Context, tx pgx.Tx, id int64, roomID, authorID uuid.UUID, content string, createdAt time.Time) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO messages (id, room_id, author_id, content, created_at) VALUES ($1,$2,$3,$4,$5)`,
		id, roomID, authorID, content, createdAt)
	return err
}

// InsertDMMessageTx inserts a DM message within tx, the DM counterpart of
// InsertRoomMessageTx.
func (r *Repository) InsertDMMessageTx(ctx context.Context, tx pgx.Tx, id int64, channelID, authorID uuid.UUID, content string, createdAt time.Time) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO dm_messages (id, channel_id, author_id, content, created_at) VALUES ($1,$2,$3,$4,$5)`,
		id, channelID, authorID, content, createdAt)
	return err
}

// GetPollFlags reads just the is_closed and allows_multiple flags of a poll, used
// to validate a vote before opening a transaction.
func (r *Repository) GetPollFlags(ctx context.Context, pollID uuid.UUID) (isClosed, allowsMultiple bool, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT is_closed, allows_multiple FROM polls WHERE id = $1`, pollID).Scan(&isClosed, &allowsMultiple)
	return
}

// DeleteUserPollVotes removes a user's existing votes on a poll within tx, used to
// replace prior votes on single-choice polls before recording the new one.
func (r *Repository) DeleteUserPollVotes(ctx context.Context, tx pgx.Tx, pollID, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM poll_votes WHERE poll_id = $1 AND user_id = $2`, pollID, userID)
	return err
}

// InsertPollVoteTx records a single vote within tx, using ON CONFLICT DO NOTHING so
// re-voting the same option is idempotent.
func (r *Repository) InsertPollVoteTx(ctx context.Context, tx pgx.Tx, pollID, userID uuid.UUID, optionID int32) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO poll_votes (poll_id, user_id, option_id) VALUES ($1,$2,$3)
		 ON CONFLICT DO NOTHING`, pollID, userID, optionID)
	return err
}

// RecalcPollCountsTx recomputes, within tx, each option's vote_count and the poll's
// total_voters (distinct voters) from the poll_votes table, keeping the cached
// tallies consistent after votes change.
func (r *Repository) RecalcPollCountsTx(ctx context.Context, tx pgx.Tx, pollID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE poll_options SET vote_count = (SELECT COUNT(*) FROM poll_votes WHERE poll_id = poll_options.poll_id AND option_id = poll_options.option_id) WHERE poll_id = $1`, pollID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE polls SET total_voters = (SELECT COUNT(DISTINCT user_id) FROM poll_votes WHERE poll_id = $1) WHERE id = $1`, pollID)
	return err
}

// LoadPoll returns a poll's header, its options ordered by option_id, and the
// option IDs the given user voted for (empty if none). The header query error is
// returned; option/vote scan failures are skipped rather than fatal.
func (r *Repository) LoadPoll(ctx context.Context, pollID, userID uuid.UUID) (*PollRow, []PollOptionRow, []int, error) {
	var poll PollRow
	err := r.pool.QueryRow(ctx,
		`SELECT question, poll_type, is_anonymous, allows_multiple, correct_option,
		 explanation, close_date, is_closed, total_voters
		 FROM polls WHERE id = $1`, pollID,
	).Scan(&poll.Question, &poll.PollType, &poll.IsAnonymous, &poll.AllowsMultiple,
		&poll.CorrectOption, &poll.Explanation, &poll.CloseDate, &poll.IsClosed, &poll.TotalVoters)
	if err != nil {
		return nil, nil, nil, err
	}
	poll.ID = pollID

	optRows, err := r.pool.Query(ctx,
		`SELECT option_id, text, vote_count FROM poll_options WHERE poll_id = $1 ORDER BY option_id`, pollID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer optRows.Close()

	var options []PollOptionRow
	for optRows.Next() {
		var o PollOptionRow
		if err := optRows.Scan(&o.OptionID, &o.Text, &o.VoteCount); err != nil {
			continue
		}
		options = append(options, o)
	}

	var myVotes []int
	voteRows, _ := r.pool.Query(ctx,
		`SELECT option_id FROM poll_votes WHERE poll_id = $1 AND user_id = $2`, pollID, userID)
	if voteRows != nil {
		for voteRows.Next() {
			var optID int
			if voteRows.Scan(&optID) == nil {
				myVotes = append(myVotes, optID)
			}
		}
		voteRows.Close()
	}

	return &poll, options, myVotes, nil
}

// ClosePoll marks a poll closed, but only if creatorID owns it; a no-op otherwise
// (so non-creators cannot close a poll).
func (r *Repository) ClosePoll(ctx context.Context, pollID uuid.UUID, creatorID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE polls SET is_closed = true WHERE id = $1 AND creator_id = $2`, pollID, creatorID)
	return err
}

// CloseExpiredPolls closes every open poll whose close_date has passed; called
// periodically by the scheduler loop.
func (r *Repository) CloseExpiredPolls(ctx context.Context) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE polls SET is_closed = true WHERE is_closed = false AND close_date IS NOT NULL AND close_date <= NOW()`)
	return err
}

// DraftRow is a saved message draft scoped to a room or DM channel.
type DraftRow struct {
	RoomID    *uuid.UUID
	ChannelID *uuid.UUID
	Content   string
	ReplyToID *int64
	UpdatedAt time.Time
}

// UpsertDraft saves or replaces the user's draft for a room/channel. Because
// room_id/channel_id may be NULL, the conflict target COALESCEs them to a sentinel
// zero UUID so "no room" and "no channel" still form a unique key per user.
func (r *Repository) UpsertDraft(ctx context.Context, userID uuid.UUID, roomID, channelID *uuid.UUID, content string, replyToID *int64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO drafts (user_id, room_id, channel_id, content, reply_to_message_id, updated_at)
		 VALUES ($1,$2,$3,$4,$5,NOW())
		 ON CONFLICT (user_id, COALESCE(room_id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(channel_id, '00000000-0000-0000-0000-000000000000'::uuid))
		 DO UPDATE SET content = $4, reply_to_message_id = $5, updated_at = NOW()`,
		userID, roomID, channelID, content, replyToID)
	return err
}

// ListDrafts returns the user's non-empty drafts, newest-updated first (empty
// drafts are filtered out). Rows that fail to scan are skipped.
func (r *Repository) ListDrafts(ctx context.Context, userID uuid.UUID) ([]DraftRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT room_id, channel_id, content, reply_to_message_id, updated_at
		 FROM drafts WHERE user_id = $1 AND content != '' ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DraftRow
	for rows.Next() {
		var row DraftRow
		if err := rows.Scan(&row.RoomID, &row.ChannelID, &row.Content, &row.ReplyToID, &row.UpdatedAt); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

// DeleteDraft removes the user's draft for a room/channel, matching NULL scopes via
// the same COALESCE-to-sentinel scheme as UpsertDraft.
func (r *Repository) DeleteDraft(ctx context.Context, userID uuid.UUID, roomID, channelID *uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM drafts WHERE user_id = $1
		 AND COALESCE(room_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE($2, '00000000-0000-0000-0000-000000000000'::uuid)
		 AND COALESCE(channel_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE($3, '00000000-0000-0000-0000-000000000000'::uuid)`,
		userID, roomID, channelID)
	return err
}

// NotifOverrideRow is a per-user notification override for a room or channel.
// MuteUntil is nil when not muted; SuppressEveryone drops @everyone pings.
type NotifOverrideRow struct {
	RoomID           *uuid.UUID
	ChannelID        *uuid.UUID
	OverrideLevel    string
	MuteUntil        *time.Time
	SuppressEveryone bool
}

// UpsertNotificationOverride saves or replaces the user's override for a
// room/channel, using the same COALESCE-to-sentinel unique key as drafts so NULL
// scopes still upsert correctly.
func (r *Repository) UpsertNotificationOverride(ctx context.Context, userID uuid.UUID, roomID, channelID *uuid.UUID, level string, muteUntil *time.Time, suppress bool) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO notification_overrides (user_id, room_id, channel_id, override_level, mute_until, suppress_everyone)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (user_id, COALESCE(room_id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(channel_id, '00000000-0000-0000-0000-000000000000'::uuid))
		 DO UPDATE SET override_level = $4, mute_until = $5, suppress_everyone = $6`,
		userID, roomID, channelID, level, muteUntil, suppress)
	return err
}

// ListNotificationOverrides returns all of the user's notification overrides. Rows
// that fail to scan are skipped.
func (r *Repository) ListNotificationOverrides(ctx context.Context, userID uuid.UUID) ([]NotifOverrideRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT room_id, channel_id, override_level, mute_until, suppress_everyone
		 FROM notification_overrides WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []NotifOverrideRow
	for rows.Next() {
		var row NotifOverrideRow
		if err := rows.Scan(&row.RoomID, &row.ChannelID, &row.OverrideLevel, &row.MuteUntil, &row.SuppressEveryone); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

// MediaAttachmentRow is one message attachment; Width/Height are nil for
// non-dimensioned files.
type MediaAttachmentRow struct {
	ID          uuid.UUID
	MessageID   int64
	URL         string
	Filename    string
	ContentType string
	Size        int64
	Width       *int
	Height      *int
	CreatedAt   time.Time
}

// ListChannelMedia returns attachments for a room or channel (exactly one of
// roomID/channelID must be non-nil, else an error), newest-first up to limit.
// mediaType>0 filters by kind: 1=image, 2=video, 3=other (by content_type).
func (r *Repository) ListChannelMedia(ctx context.Context, roomID, channelID *uuid.UUID, mediaType int, limit int) ([]MediaAttachmentRow, error) {
	var query string
	var args []interface{}

	if roomID != nil {
		query = `SELECT ma.id, ma.message_id, ma.url, ma.filename, ma.content_type, ma.size, ma.width, ma.height, ma.created_at
				 FROM message_attachments ma
				 JOIN messages m ON ma.message_id = m.id
				 WHERE m.room_id = $1 AND m.deleted_at IS NULL`
		args = []interface{}{*roomID}
	} else if channelID != nil {
		query = `SELECT dma.id, dma.message_id, dma.url, dma.filename, dma.content_type, dma.size, dma.width, dma.height, dma.created_at
				 FROM dm_message_attachments dma
				 JOIN dm_messages dm ON dma.message_id = dm.id
				 WHERE dm.channel_id = $1 AND dm.deleted_at IS NULL`
		args = []interface{}{*channelID}
	} else {
		return nil, fmt.Errorf("room_id or channel_id required")
	}

	if mediaType > 0 {
		switch mediaType {
		case 1:
			query += " AND content_type LIKE 'image/%'"
		case 2:
			query += " AND content_type LIKE 'video/%'"
		case 3:
			query += " AND content_type NOT LIKE 'image/%' AND content_type NOT LIKE 'video/%'"
		}
	}

	query += " ORDER BY created_at DESC LIMIT $2"
	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MediaAttachmentRow
	for rows.Next() {
		var row MediaAttachmentRow
		if err := rows.Scan(&row.ID, &row.MessageID, &row.URL, &row.Filename, &row.ContentType, &row.Size, &row.Width, &row.Height, &row.CreatedAt); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

// SetSlowMode sets a room's slow-mode interval (seconds); 0 disables slow mode.
func (r *Repository) SetSlowMode(ctx context.Context, roomID uuid.UUID, interval int32) error {
	_, err := r.pool.Exec(ctx, `UPDATE rooms SET slow_mode_interval = $2 WHERE id = $1`, roomID, interval)
	return err
}

// SuppressLinkPreview flags a single link preview (identified by message + url_hash)
// as suppressed so it is no longer rendered.
func (r *Repository) SuppressLinkPreview(ctx context.Context, messageID int64, urlHash string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE message_link_previews SET suppressed = true WHERE message_id = $1 AND url_hash = $2`,
		messageID, urlHash)
	return err
}

// StickerPackRow is a sticker pack's metadata (its stickers are loaded separately).
type StickerPackRow struct {
	ID          uuid.UUID
	Name        string
	Description string
	CreatorID   uuid.UUID
}

// StickerRow is one sticker within a pack; FormatType encodes the asset format.
type StickerRow struct {
	ID         uuid.UUID
	PackID     uuid.UUID
	Name       string
	Tags       string
	FormatType int
	FileURL    string
	Width      int
	Height     int
}

// ListUserStickerPacks returns the sticker packs the user has added (joined via
// user_sticker_packs). Rows that fail to scan are skipped.
func (r *Repository) ListUserStickerPacks(ctx context.Context, userID uuid.UUID) ([]StickerPackRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT sp.id, sp.name, sp.description, sp.creator_id
		 FROM sticker_packs sp
		 JOIN user_sticker_packs usp ON sp.id = usp.pack_id
		 WHERE usp.user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []StickerPackRow
	for rows.Next() {
		var row StickerPackRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Description, &row.CreatorID); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

// ListStickersForPack returns all stickers in a pack; PackID is filled from the
// argument. Rows that fail to scan are skipped.
func (r *Repository) ListStickersForPack(ctx context.Context, packID uuid.UUID) ([]StickerRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, name, tags, format_type, file_url, width, height FROM stickers WHERE pack_id = $1`, packID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []StickerRow
	for rows.Next() {
		var row StickerRow
		row.PackID = packID
		if err := rows.Scan(&row.ID, &row.Name, &row.Tags, &row.FormatType, &row.FileURL, &row.Width, &row.Height); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

// BeginTx starts a database transaction the caller must commit or roll back.
func (r *Repository) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

// GetRoomSlowMode returns a room's slow-mode interval in seconds, treating a NULL
// column as 0 (disabled).
func (r *Repository) GetRoomSlowMode(ctx context.Context, roomID uuid.UUID) (int, error) {
	var interval int
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(slow_mode_interval, 0) FROM rooms WHERE id = $1`, roomID).Scan(&interval)
	return interval, err
}
