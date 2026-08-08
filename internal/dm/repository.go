package dm

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/editing"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/media"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DMChannel is a 1:1 direct-message channel between two users. The pair is
// stored canonically ordered (User1ID < User2ID) so a channel is unique per
// unordered pair. HasActiveCall is populated only by queries that join dm_calls.
type DMChannel struct {
	ID            uuid.UUID
	User1ID       uuid.UUID
	User2ID       uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
	HasActiveCall bool
}

// DMChannelWithUser pairs a channel with a denormalized snapshot of the other
// participant (the counterpart to the requesting user), for rendering DM lists
// without a second lookup.
type DMChannelWithUser struct {
	Channel          *DMChannel
	OtherUserID      uuid.UUID
	OtherUserHandle  string
	OtherUserDisplay string
	OtherUserAvatar  string
	OtherUserStatus  string
}

// ReadReceipt records that a user has read up to a message, with the read time.
type ReadReceipt struct {
	UserID uuid.UUID
	ReadAt time.Time
}

// DMMessage is a direct-message message. ID is an int64 Snowflake whose embedded
// timestamp also drives CreatedAt. EditedAt/DeletedAt are nil until edited or
// soft-deleted; the Forward* and Reply* fields hold optional forward/reply
// metadata, and Attachments/Reactions/Mentions/ReadBy are loaded separately.
type DMMessage struct {
	ID                   int64
	ChannelID            uuid.UUID
	AuthorID             uuid.UUID
	Content              string
	CreatedAt            time.Time
	EditedAt             *time.Time
	DeletedAt            *time.Time
	ReplyToID            *int64
	ReplyCount           int32
	Pinned               bool
	Attachments          []DMAttachment
	Reactions            []DMReaction
	Mentions             []uuid.UUID
	ReadBy               []ReadReceipt
	ForwardFromUserID    *uuid.UUID
	ForwardFromUserName  *string
	ForwardFromChannelID *uuid.UUID
	ForwardFromMsgID     *int64
	ForwardOriginalTS    *time.Time
	MediaGroupID         *string
	ReplyQuotedContent   *string
	ReplyMentionAuthor   bool
	EditCount            int32
}

// DMAttachment is a stored file linked to a DM message. Width/Height are zero
// for non-image content.
type DMAttachment struct {
	ID          uuid.UUID
	MessageID   int64
	URL         string
	Filename    string
	ContentType string
	Size        int64
	Width       int
	Height      int
	CreatedAt   time.Time
}

// DMReaction is one user's emoji reaction on a DM message; the
// (message, user, emoji) triple is unique.
type DMReaction struct {
	ID        uuid.UUID
	MessageID int64
	UserID    uuid.UUID
	Emoji     string
	CreatedAt time.Time
}

// DMCall is a voice call within a DM channel. EndedAt is nil while the call is
// active; VoiceServerID points at the assigned voice server (nullable until
// assigned).
type DMCall struct {
	ID            uuid.UUID
	ChannelID     uuid.UUID
	StartedBy     uuid.UUID
	StartedAt     time.Time
	EndedAt       *time.Time
	VoiceServerID *uuid.UUID
}

// dmMessageSelectColumns is the shared SELECT list (aliased m) for scanning a
// DMMessage via scanDMMessage. It derives pinned from an EXISTS subquery and
// COALESCEs nullable columns so every row scans into non-pointer fields.
const dmMessageSelectColumns = `
	m.id,
	m.channel_id,
	m.author_id,
	m.content,
	m.created_at,
	m.edited_at,
	m.deleted_at,
	m.reply_to_id,
	m.reply_count,
	COALESCE((SELECT true FROM dm_pinned_messages WHERE message_id = m.id), false) as pinned,
	m.forwarded_from_user_id,
	m.forwarded_from_user_name,
	m.forwarded_from_channel_id,
	m.forwarded_from_message_id,
	m.forwarded_original_timestamp,
	m.media_group_id,
	m.reply_quoted_content,
	COALESCE(m.reply_mention_author, true),
	COALESCE(m.edit_count, 0)
`

// rowScanner abstracts pgx.Row and pgx.Rows so scanDMMessage works with both a
// single QueryRow result and rows iterated from Query.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanDMMessage scans one row selected with dmMessageSelectColumns into msg. The
// scan order must match that column list exactly.
func scanDMMessage(scanner rowScanner, msg *DMMessage) error {
	return scanner.Scan(
		&msg.ID,
		&msg.ChannelID,
		&msg.AuthorID,
		&msg.Content,
		&msg.CreatedAt,
		&msg.EditedAt,
		&msg.DeletedAt,
		&msg.ReplyToID,
		&msg.ReplyCount,
		&msg.Pinned,
		&msg.ForwardFromUserID,
		&msg.ForwardFromUserName,
		&msg.ForwardFromChannelID,
		&msg.ForwardFromMsgID,
		&msg.ForwardOriginalTS,
		&msg.MediaGroupID,
		&msg.ReplyQuotedContent,
		&msg.ReplyMentionAuthor,
		&msg.EditCount,
	)
}

// Repository persists DM channels and their voice calls. Messages live in the
// separate MessageRepository; this package deliberately uses two repositories.
type Repository struct {
	pool *pgxpool.Pool
}

// MessageRepository persists DM messages and their attachments, reactions,
// mentions, and pins. It owns the Snowflake generator, the edit-history recorder,
// and the media indexer used inside its write transactions.
type MessageRepository struct {
	pool      *pgxpool.Pool
	snowflake *infra.SnowflakeGenerator
	recorder  *editing.Recorder
	media     *media.Indexer
}

// NewRepository constructs the DM channel/call Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// NewMessageRepository constructs the DM MessageRepository with its own media
// indexer; snowflake generates message IDs and rec records edit history.
func NewMessageRepository(pool *pgxpool.Pool, snowflake *infra.SnowflakeGenerator, rec *editing.Recorder) *MessageRepository {
	return &MessageRepository{pool: pool, snowflake: snowflake, recorder: rec, media: media.NewIndexer()}
}

// GetOrCreate returns the DM channel for a pair of users, creating it if absent.
// The two ids are normalized so the smaller UUID string is always user1_id,
// giving each unordered pair a single canonical row and making lookup order-
// independent.
func (r *Repository) GetOrCreate(ctx context.Context, user1ID, user2ID uuid.UUID) (*DMChannel, error) {
	if user1ID.String() > user2ID.String() {
		user1ID, user2ID = user2ID, user1ID
	}

	query := `
		SELECT id, user1_id, user2_id, created_at, updated_at
		FROM dm_channels
		WHERE user1_id = $1 AND user2_id = $2
	`

	channel := &DMChannel{}
	err := r.pool.QueryRow(ctx, query, user1ID, user2ID).Scan(
		&channel.ID, &channel.User1ID, &channel.User2ID,
		&channel.CreatedAt, &channel.UpdatedAt,
	)

	if err == nil {
		return channel, nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}

	insertQuery := `
		INSERT INTO dm_channels (id, user1_id, user2_id)
		VALUES ($1, $2, $3)
		RETURNING id, user1_id, user2_id, created_at, updated_at
	`
	channel.ID = uuid.New()
	err = r.pool.QueryRow(ctx, insertQuery, channel.ID, user1ID, user2ID).Scan(
		&channel.ID, &channel.User1ID, &channel.User2ID,
		&channel.CreatedAt, &channel.UpdatedAt,
	)
	return channel, err
}

// GetByID loads a channel by id, computing HasActiveCall from an EXISTS check
// against dm_calls. Returns NotFound when the channel does not exist.
func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*DMChannel, error) {
	query := `
		SELECT dc.id, dc.user1_id, dc.user2_id, dc.created_at, dc.updated_at,
		       EXISTS(SELECT 1 FROM dm_calls WHERE channel_id = dc.id AND ended_at IS NULL) as has_active_call
		FROM dm_channels dc
		WHERE dc.id = $1
	`
	channel := &DMChannel{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&channel.ID, &channel.User1ID, &channel.User2ID,
		&channel.CreatedAt, &channel.UpdatedAt, &channel.HasActiveCall,
	)
	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("DM channel not found")
	}
	return channel, err
}

// ListByUser returns every DM channel the user participates in, each joined with
// the other participant's profile, ordered by most recently updated first.
func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID) ([]*DMChannelWithUser, error) {
	query := `
		SELECT 
			dc.id, dc.user1_id, dc.user2_id, dc.created_at, dc.updated_at,
			u.id as other_user_id, u.handle, u.display_name, u.avatar_url, u.status
		FROM dm_channels dc
		JOIN users u ON (
			CASE 
				WHEN dc.user1_id = $1 THEN dc.user2_id = u.id
				ELSE dc.user1_id = u.id
			END
		)
		WHERE dc.user1_id = $1 OR dc.user2_id = $1
		ORDER BY dc.updated_at DESC
	`
	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []*DMChannelWithUser
	for rows.Next() {
		ch := &DMChannelWithUser{Channel: &DMChannel{}}
		err := rows.Scan(
			&ch.Channel.ID, &ch.Channel.User1ID, &ch.Channel.User2ID,
			&ch.Channel.CreatedAt, &ch.Channel.UpdatedAt,
			&ch.OtherUserID, &ch.OtherUserHandle, &ch.OtherUserDisplay,
			&ch.OtherUserAvatar, &ch.OtherUserStatus,
		)
		if err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

// UpdateTimestamp bumps a channel's updated_at to now, used to reorder DM lists
// on activity.
func (r *Repository) UpdateTimestamp(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE dm_channels SET updated_at = NOW() WHERE id = $1`, id)
	return err
}

// Delete permanently removes a channel, returning NotFound if no row matched.
func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	result, err := r.pool.Exec(ctx, `DELETE FROM dm_channels WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("DM channel not found")
	}
	return nil
}

// IsParticipant reports whether userID is one of the channel's two participants.
func (r *Repository) IsParticipant(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM dm_channels
			WHERE id = $1 AND (user1_id = $2 OR user2_id = $2)
		)
	`, channelID, userID).Scan(&exists)
	return exists, err
}

// CreateCall inserts a new active call for a channel. A unique-violation (23505)
// from the DB — meaning a call is already active — is translated to a Conflict
// error so callers can distinguish contention from other failures.
func (r *Repository) CreateCall(ctx context.Context, channelID, startedBy uuid.UUID, voiceServerID *uuid.UUID) (*DMCall, error) {
	call := &DMCall{
		ID:            uuid.New(),
		ChannelID:     channelID,
		StartedBy:     startedBy,
		StartedAt:     time.Now(),
		VoiceServerID: voiceServerID,
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO dm_calls (id, channel_id, started_by, started_at, voice_server_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, channel_id, started_by, started_at, voice_server_id
	`, call.ID, call.ChannelID, call.StartedBy, call.StartedAt, call.VoiceServerID).Scan(
		&call.ID, &call.ChannelID, &call.StartedBy, &call.StartedAt, &call.VoiceServerID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errorsAs(err, &pgErr) && pgErr.Code == "23505" {
			return nil, errors.Conflict("call already active")
		}
		return nil, err
	}
	return call, err
}

// GetActiveCall returns the channel's current active (not-yet-ended) call, or
// (nil, nil) when there is none — the no-rows case is not an error.
func (r *Repository) GetActiveCall(ctx context.Context, channelID uuid.UUID) (*DMCall, error) {
	call := &DMCall{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, channel_id, started_by, started_at, ended_at, voice_server_id
		FROM dm_calls
		WHERE channel_id = $1 AND ended_at IS NULL
		ORDER BY started_at DESC
		LIMIT 1
	`, channelID).Scan(
		&call.ID, &call.ChannelID, &call.StartedBy, &call.StartedAt, &call.EndedAt, &call.VoiceServerID,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return call, err
}

// EndCall marks a specific call ended (setting ended_at), idempotent: it only
// touches a still-active call.
func (r *Repository) EndCall(ctx context.Context, callID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE dm_calls SET ended_at = NOW() WHERE id = $1 AND ended_at IS NULL`, callID)
	return err
}

// EndActiveCall ends whichever call is currently active for a channel, by
// channel rather than call id.
func (r *Repository) EndActiveCall(ctx context.Context, channelID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE dm_calls SET ended_at = NOW() WHERE channel_id = $1 AND ended_at IS NULL`, channelID)
	return err
}

// Create inserts a DM message with its attachments, mentions, and reply-count
// bump in a single transaction, and touches the channel's updated_at. It
// generates a Snowflake ID when unset and derives CreatedAt from that ID's
// embedded timestamp (not the DB clock). Attachments also get indexed into the
// channel media table; the whole insert rolls back on any error.
func (r *MessageRepository) Create(ctx context.Context, msg *DMMessage) error {
	if msg.ID == 0 {
		msg.ID = r.snowflake.Generate()
	}
	msg.CreatedAt = r.snowflake.ExtractTimestamp(msg.ID)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO dm_messages (
			id, channel_id, author_id, content, created_at, reply_to_id,
			forwarded_from_user_id, forwarded_from_user_name, forwarded_from_channel_id,
			forwarded_from_message_id, forwarded_original_timestamp,
			media_group_id, reply_quoted_content, reply_mention_author
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		msg.ID, msg.ChannelID, msg.AuthorID, msg.Content, msg.CreatedAt, msg.ReplyToID,
		msg.ForwardFromUserID, msg.ForwardFromUserName, msg.ForwardFromChannelID,
		msg.ForwardFromMsgID, msg.ForwardOriginalTS,
		msg.MediaGroupID, msg.ReplyQuotedContent, msg.ReplyMentionAuthor,
	)
	if err != nil {
		return err
	}

	for i := range msg.Attachments {
		att := &msg.Attachments[i]
		if att.ID == uuid.Nil {
			att.ID = uuid.New()
		}
		att.MessageID = msg.ID
		att.CreatedAt = msg.CreatedAt
		_, err := tx.Exec(ctx, `
			INSERT INTO dm_message_attachments (id, message_id, url, filename, content_type, size, width, height, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			att.ID, att.MessageID, att.URL, att.Filename, att.ContentType, att.Size, att.Width, att.Height, att.CreatedAt)
		if err != nil {
			return err
		}
		if err := r.media.InsertChannelTx(ctx, tx, msg.ID, msg.ChannelID, att.URL, att.ContentType, att.Width, att.Height, att.Size, att.CreatedAt); err != nil {
			return err
		}
	}

	for _, userID := range msg.Mentions {
		if _, err := tx.Exec(ctx,
			`INSERT INTO dm_message_mentions (message_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			msg.ID, userID); err != nil {
			return err
		}
	}

	if msg.ReplyToID != nil {
		if _, err := tx.Exec(ctx, `UPDATE dm_messages SET reply_count = reply_count + 1 WHERE id = $1`, *msg.ReplyToID); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE dm_channels SET updated_at = NOW() WHERE id = $1`, msg.ChannelID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// GetByID loads a single DM message and then its attachments, reactions, and
// mentions with follow-up queries (those secondary loads ignore their errors,
// leaving the slices empty on failure). Returns NotFound when the id is unknown.
func (r *MessageRepository) GetByID(ctx context.Context, id int64) (*DMMessage, error) {
	query := fmt.Sprintf(`SELECT %s FROM dm_messages m WHERE m.id = $1`, dmMessageSelectColumns)
	msg := &DMMessage{}
	err := scanDMMessage(r.pool.QueryRow(ctx, query, id), msg)
	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("message not found")
	}
	if err != nil {
		return nil, err
	}

	msg.Attachments, _ = r.GetAttachments(ctx, id)
	msg.Reactions, _ = r.GetReactions(ctx, id)
	msg.Mentions, _ = r.GetMentions(ctx, id)
	return msg, nil
}

// Update edits a message's content in a transaction: it locks the row (FOR
// UPDATE), snapshots the previous content into the edit history via the recorder
// when one is provided, then updates content, sets edited_at, and increments
// edit_count (reflected back into msg). Returns NotFound if the message is gone
// or already deleted.
func (r *MessageRepository) Update(ctx context.Context, msg *DMMessage, recorder *editing.Recorder) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var prev string
	err = tx.QueryRow(ctx,
		`SELECT content FROM dm_messages WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`,
		msg.ID,
	).Scan(&prev)
	if err == pgx.ErrNoRows {
		return errors.NotFound("message not found")
	}
	if err != nil {
		return err
	}

	if recorder != nil {
		if err := recorder.RecordDM(ctx, tx, msg.ID, prev); err != nil {
			return err
		}
	}

	now := time.Now()
	msg.EditedAt = &now
	err = tx.QueryRow(ctx,
		`UPDATE dm_messages SET content = $2, edited_at = $3, edit_count = COALESCE(edit_count, 0) + 1
		 WHERE id = $1 AND deleted_at IS NULL RETURNING edit_count`,
		msg.ID, msg.Content, msg.EditedAt,
	).Scan(&msg.EditCount)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SoftDelete marks a message deleted (sets deleted_at) without removing the row.
// Returns NotFound if it was already deleted or never existed.
func (r *MessageRepository) SoftDelete(ctx context.Context, id int64) error {
	result, err := r.pool.Exec(ctx,
		`UPDATE dm_messages SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL`,
		id, time.Now())
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("message not found")
	}
	return nil
}

// ListByChannel returns a page of a channel's non-deleted messages. beforeID and
// afterID are exclusive Snowflake cursors selecting the paging direction; the DB
// orders DESC for default/before and ASC for after (so the surplus limit+1
// boundary row is dropped in query order), after which default/before results
// are reversed so the caller always receives ascending (oldest-first) order.
func (r *MessageRepository) ListByChannel(ctx context.Context, channelID uuid.UUID, beforeID, afterID *int64, limit int) ([]*DMMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var query string
	var args []interface{}

	switch {
	case beforeID != nil:
		query = fmt.Sprintf(`SELECT %s FROM dm_messages m
			WHERE m.channel_id = $1 AND m.id < $2 AND m.deleted_at IS NULL
			ORDER BY m.id DESC LIMIT $3`, dmMessageSelectColumns)
		args = []interface{}{channelID, *beforeID, limit}
	case afterID != nil:
		query = fmt.Sprintf(`SELECT %s FROM dm_messages m
			WHERE m.channel_id = $1 AND m.id > $2 AND m.deleted_at IS NULL
			ORDER BY m.id ASC LIMIT $3`, dmMessageSelectColumns)
		args = []interface{}{channelID, *afterID, limit}
	default:
		query = fmt.Sprintf(`SELECT %s FROM dm_messages m
			WHERE m.channel_id = $1 AND m.deleted_at IS NULL
			ORDER BY m.id DESC LIMIT $2`, dmMessageSelectColumns)
		args = []interface{}{channelID, limit}
	}

	messages, err := r.queryMessages(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	// queryMessages returns rows in the query's ORDER BY order:
	//   default + before  -> DESC (newest first)
	//   after             -> ASC  (oldest first)
	// The caller passes limit+1; trimming here happens in query order so the
	// surplus element (the out-of-window boundary) is always dropped, then we
	// normalize default/before to ASC for the client.
	if beforeID == nil && afterID == nil {
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
	}
	return messages, nil
}

// GetThreadReplies returns the non-deleted replies to parentID in ascending id
// order, paginated by limit/offset.
func (r *MessageRepository) GetThreadReplies(ctx context.Context, parentID int64, limit, offset int) ([]*DMMessage, error) {
	query := fmt.Sprintf(`SELECT %s FROM dm_messages m
		WHERE m.reply_to_id = $1 AND m.deleted_at IS NULL
		ORDER BY m.id ASC LIMIT $2 OFFSET $3`, dmMessageSelectColumns)
	return r.queryMessages(ctx, query, parentID, limit, offset)
}

// Search finds non-deleted messages in a channel whose content matches query via
// a case-insensitive ILIKE substring match (no full-text index), newest first.
func (r *MessageRepository) Search(ctx context.Context, channelID uuid.UUID, query string, limit int) ([]*DMMessage, error) {
	sqlQuery := fmt.Sprintf(`SELECT %s FROM dm_messages m
		WHERE m.channel_id = $1 AND m.deleted_at IS NULL AND m.content ILIKE '%%' || $2 || '%%'
		ORDER BY m.id DESC LIMIT $3`, dmMessageSelectColumns)
	return r.queryMessages(ctx, sqlQuery, channelID, query, limit)
}

// ListPinnedMessages returns a channel's pinned messages, most recently pinned
// first, joining dm_pinned_messages to the message rows.
func (r *MessageRepository) ListPinnedMessages(ctx context.Context, channelID uuid.UUID) ([]*DMMessage, error) {
	query := fmt.Sprintf(`SELECT %s FROM dm_messages m
		INNER JOIN dm_pinned_messages pm ON m.id = pm.message_id
		WHERE pm.channel_id = $1 AND m.deleted_at IS NULL
		ORDER BY pm.pinned_at DESC`, dmMessageSelectColumns)
	return r.queryMessages(ctx, query, channelID)
}

// queryMessages runs a message-selecting query and, in one batch per relation,
// back-fills the attachments, reactions, and mentions for every returned message
// (avoiding an N+1 of per-message follow-ups). Rows come back in the query's own
// ORDER BY order; reordering, if any, is the caller's responsibility.
func (r *MessageRepository) queryMessages(ctx context.Context, query string, args ...interface{}) ([]*DMMessage, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*DMMessage
	var messageIDs []int64
	for rows.Next() {
		msg := &DMMessage{}
		if err := scanDMMessage(rows, msg); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
		messageIDs = append(messageIDs, msg.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(messageIDs) > 0 {
		attachmentsMap, err := r.GetAttachmentsBatch(ctx, messageIDs)
		if err != nil {
			return nil, err
		}
		reactionsMap, err := r.GetReactionsBatch(ctx, messageIDs)
		if err != nil {
			return nil, err
		}
		mentionsMap, err := r.GetMentionsBatch(ctx, messageIDs)
		if err != nil {
			return nil, err
		}
		for _, msg := range messages {
			msg.Attachments = attachmentsMap[msg.ID]
			msg.Reactions = reactionsMap[msg.ID]
			msg.Mentions = mentionsMap[msg.ID]
		}
	}
	return messages, nil
}

// GetAttachmentsBatch loads attachments for many messages at once, keyed by
// message id, using a single ANY($1) query. Empty input yields an empty map.
func (r *MessageRepository) GetAttachmentsBatch(ctx context.Context, messageIDs []int64) (map[int64][]DMAttachment, error) {
	if len(messageIDs) == 0 {
		return make(map[int64][]DMAttachment), nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, message_id, url, filename, content_type, size, width, height, created_at
		FROM dm_message_attachments WHERE message_id = ANY($1) ORDER BY created_at ASC`, messageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64][]DMAttachment)
	for rows.Next() {
		var att DMAttachment
		if err := rows.Scan(&att.ID, &att.MessageID, &att.URL, &att.Filename, &att.ContentType, &att.Size, &att.Width, &att.Height, &att.CreatedAt); err != nil {
			return nil, err
		}
		result[att.MessageID] = append(result[att.MessageID], att)
	}
	return result, rows.Err()
}

// GetReactionsBatch loads reactions for many messages at once, keyed by message
// id. Empty input yields an empty map.
func (r *MessageRepository) GetReactionsBatch(ctx context.Context, messageIDs []int64) (map[int64][]DMReaction, error) {
	if len(messageIDs) == 0 {
		return make(map[int64][]DMReaction), nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, message_id, user_id, emoji, created_at
		FROM dm_message_reactions WHERE message_id = ANY($1) ORDER BY created_at ASC`, messageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64][]DMReaction)
	for rows.Next() {
		var dr DMReaction
		if err := rows.Scan(&dr.ID, &dr.MessageID, &dr.UserID, &dr.Emoji, &dr.CreatedAt); err != nil {
			return nil, err
		}
		result[dr.MessageID] = append(result[dr.MessageID], dr)
	}
	return result, rows.Err()
}

// GetMentionsBatch loads mentioned user ids for many messages at once, keyed by
// message id. Empty input yields an empty map.
func (r *MessageRepository) GetMentionsBatch(ctx context.Context, messageIDs []int64) (map[int64][]uuid.UUID, error) {
	if len(messageIDs) == 0 {
		return make(map[int64][]uuid.UUID), nil
	}
	rows, err := r.pool.Query(ctx, `SELECT message_id, user_id FROM dm_message_mentions WHERE message_id = ANY($1)`, messageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64][]uuid.UUID)
	for rows.Next() {
		var msgID int64
		var userID uuid.UUID
		if err := rows.Scan(&msgID, &userID); err != nil {
			return nil, err
		}
		result[msgID] = append(result[msgID], userID)
	}
	return result, rows.Err()
}

// GetMentions returns the user ids mentioned in a single message.
func (r *MessageRepository) GetMentions(ctx context.Context, messageID int64) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `SELECT user_id FROM dm_message_mentions WHERE message_id = $1`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var mentions []uuid.UUID
	for rows.Next() {
		var userID uuid.UUID
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		mentions = append(mentions, userID)
	}
	return mentions, rows.Err()
}

// GetAttachments returns the attachments of a single message, oldest first.
func (r *MessageRepository) GetAttachments(ctx context.Context, messageID int64) ([]DMAttachment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, message_id, url, filename, content_type, size, width, height, created_at
		FROM dm_message_attachments WHERE message_id = $1 ORDER BY created_at`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attachments []DMAttachment
	for rows.Next() {
		var att DMAttachment
		if err := rows.Scan(&att.ID, &att.MessageID, &att.URL, &att.Filename, &att.ContentType, &att.Size, &att.Width, &att.Height, &att.CreatedAt); err != nil {
			return nil, err
		}
		attachments = append(attachments, att)
	}
	return attachments, rows.Err()
}

// GetReactions returns the reactions on a single message, oldest first.
func (r *MessageRepository) GetReactions(ctx context.Context, messageID int64) ([]DMReaction, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, message_id, user_id, emoji, created_at
		FROM dm_message_reactions WHERE message_id = $1 ORDER BY created_at`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reactions []DMReaction
	for rows.Next() {
		var dr DMReaction
		if err := rows.Scan(&dr.ID, &dr.MessageID, &dr.UserID, &dr.Emoji, &dr.CreatedAt); err != nil {
			return nil, err
		}
		reactions = append(reactions, dr)
	}
	return reactions, rows.Err()
}

// AddReaction records a user's emoji reaction. The insert is ON CONFLICT DO
// NOTHING on the (message, user, emoji) unique key, so a duplicate produces no
// row and is reported as a Conflict error rather than a silent success.
func (r *MessageRepository) AddReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (*DMReaction, error) {
	reaction := &DMReaction{
		ID: uuid.New(), MessageID: messageID, UserID: userID, Emoji: emoji, CreatedAt: time.Now(),
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO dm_message_reactions (id, message_id, user_id, emoji, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (message_id, user_id, emoji) DO NOTHING
		RETURNING created_at`,
		reaction.ID, messageID, userID, emoji, reaction.CreatedAt).Scan(&reaction.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, errors.Conflict("reaction already exists")
	}
	if err != nil {
		return nil, err
	}
	return reaction, nil
}

// RemoveReaction deletes a user's emoji reaction and returns the removed row's
// id, or NotFound when no such reaction exists.
func (r *MessageRepository) RemoveReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (uuid.UUID, error) {
	var reactionID uuid.UUID
	err := r.pool.QueryRow(ctx, `
		DELETE FROM dm_message_reactions
		WHERE message_id = $1 AND user_id = $2 AND emoji = $3
		RETURNING id`, messageID, userID, emoji).Scan(&reactionID)
	if err == pgx.ErrNoRows {
		return uuid.Nil, errors.NotFound("reaction not found")
	}
	if err != nil {
		return uuid.Nil, err
	}
	return reactionID, nil
}

// PinMessage pins a message in a channel (recording who pinned it), idempotent
// via ON CONFLICT DO NOTHING on (channel, message).
func (r *MessageRepository) PinMessage(ctx context.Context, channelID uuid.UUID, messageID int64, pinnedBy uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO dm_pinned_messages (channel_id, message_id, pinned_by)
		VALUES ($1, $2, $3) ON CONFLICT (channel_id, message_id) DO NOTHING`,
		channelID, messageID, pinnedBy)
	return err
}

// UnpinMessage removes a channel's pin for a message, returning NotFound if it
// was not pinned.
func (r *MessageRepository) UnpinMessage(ctx context.Context, channelID uuid.UUID, messageID int64) error {
	result, err := r.pool.Exec(ctx, `DELETE FROM dm_pinned_messages WHERE channel_id = $1 AND message_id = $2`, channelID, messageID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("pinned message not found")
	}
	return nil
}

// errorsAs is a thin wrapper over errors.As, letting this file match a pgconn
// error type without importing the standard errors package under its own name
// (it is aliased to stderrors to avoid clashing with the common errors package).
func errorsAs(err error, target interface{}) bool {
	return stderrors.As(err, target)
}

// UpdateCallServer records which voice server a still-active call was assigned
// to, used when a call is (re)placed on a server after creation.
func (r *Repository) UpdateCallServer(ctx context.Context, callID uuid.UUID, serverID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE dm_calls SET voice_server_id = $2 WHERE id = $1 AND ended_at IS NULL`, callID, serverID)
	return err
}
