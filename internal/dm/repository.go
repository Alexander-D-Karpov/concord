package dm

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DMChannel struct {
	ID            uuid.UUID
	User1ID       uuid.UUID
	User2ID       uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
	HasActiveCall bool
}

type DMChannelWithUser struct {
	Channel          *DMChannel
	OtherUserID      uuid.UUID
	OtherUserHandle  string
	OtherUserDisplay string
	OtherUserAvatar  string
	OtherUserStatus  string
}

type ReadReceipt struct {
	UserID uuid.UUID
	ReadAt time.Time
}

type DMMessage struct {
	ID          int64
	ChannelID   uuid.UUID
	AuthorID    uuid.UUID
	Content     string
	CreatedAt   time.Time
	EditedAt    *time.Time
	DeletedAt   *time.Time
	ReplyToID   *int64
	ReplyCount  int32
	Pinned      bool
	Attachments []DMAttachment
	Reactions   []DMReaction
	Mentions    []uuid.UUID
	ReadBy      []ReadReceipt
}

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

type DMReaction struct {
	ID        uuid.UUID
	MessageID int64
	UserID    uuid.UUID
	Emoji     string
	CreatedAt time.Time
}

type DMCall struct {
	ID            uuid.UUID
	ChannelID     uuid.UUID
	StartedBy     uuid.UUID
	StartedAt     time.Time
	EndedAt       *time.Time
	VoiceServerID *uuid.UUID
}

type Repository struct {
	pool *pgxpool.Pool
}

type MessageRepository struct {
	pool      *pgxpool.Pool
	snowflake *infra.SnowflakeGenerator
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func NewMessageRepository(pool *pgxpool.Pool, snowflake *infra.SnowflakeGenerator) *MessageRepository {
	return &MessageRepository{pool: pool, snowflake: snowflake}
}

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

func (r *Repository) UpdateTimestamp(ctx context.Context, id uuid.UUID) error {
	query := `UPDATE dm_channels SET updated_at = NOW() WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, id)
	return err
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM dm_channels WHERE id = $1`
	result, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("DM channel not found")
	}
	return nil
}

func (r *Repository) IsParticipant(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1 FROM dm_channels
			WHERE id = $1 AND (user1_id = $2 OR user2_id = $2)
		)
	`

	var exists bool
	err := r.pool.QueryRow(ctx, query, channelID, userID).Scan(&exists)
	return exists, err
}

func (r *Repository) CreateCall(ctx context.Context, channelID, startedBy uuid.UUID, voiceServerID *uuid.UUID) (*DMCall, error) {
	call := &DMCall{
		ID:            uuid.New(),
		ChannelID:     channelID,
		StartedBy:     startedBy,
		StartedAt:     time.Now(),
		VoiceServerID: voiceServerID,
	}

	query := `
		INSERT INTO dm_calls (id, channel_id, started_by, started_at, voice_server_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, channel_id, started_by, started_at, voice_server_id
	`

	err := r.pool.QueryRow(ctx, query, call.ID, call.ChannelID, call.StartedBy, call.StartedAt, call.VoiceServerID).Scan(
		&call.ID, &call.ChannelID, &call.StartedBy, &call.StartedAt, &call.VoiceServerID,
	)

	return call, err
}

func (r *Repository) GetActiveCall(ctx context.Context, channelID uuid.UUID) (*DMCall, error) {
	query := `
		SELECT id, channel_id, started_by, started_at, ended_at, voice_server_id
		FROM dm_calls
		WHERE channel_id = $1 AND ended_at IS NULL
		ORDER BY started_at DESC
		LIMIT 1
	`

	call := &DMCall{}
	err := r.pool.QueryRow(ctx, query, channelID).Scan(
		&call.ID, &call.ChannelID, &call.StartedBy, &call.StartedAt, &call.EndedAt, &call.VoiceServerID,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}

	return call, err
}

func (r *Repository) EndCall(ctx context.Context, callID uuid.UUID) error {
	query := `UPDATE dm_calls SET ended_at = NOW() WHERE id = $1 AND ended_at IS NULL`
	_, err := r.pool.Exec(ctx, query, callID)
	return err
}

func (r *Repository) EndActiveCall(ctx context.Context, channelID uuid.UUID) error {
	query := `UPDATE dm_calls SET ended_at = NOW() WHERE channel_id = $1 AND ended_at IS NULL`
	_, err := r.pool.Exec(ctx, query, channelID)
	return err
}

func (r *MessageRepository) Create(ctx context.Context, msg *DMMessage) error {
	if msg.ID == 0 {
		msg.ID = r.snowflake.Generate()
	}
	msg.CreatedAt = r.snowflake.ExtractTimestamp(msg.ID)

	query := `
		INSERT INTO dm_messages (id, channel_id, author_id, content, created_at, reply_to_id)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := r.pool.Exec(ctx, query, msg.ID, msg.ChannelID, msg.AuthorID, msg.Content, msg.CreatedAt, msg.ReplyToID)
	if err != nil {
		return err
	}

	if len(msg.Attachments) > 0 {
		attachQuery := `
			INSERT INTO dm_message_attachments (id, message_id, url, filename, content_type, size, width, height, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`
		for i := range msg.Attachments {
			att := &msg.Attachments[i]
			if att.ID == uuid.Nil {
				att.ID = uuid.New()
			}
			att.MessageID = msg.ID
			att.CreatedAt = msg.CreatedAt
			_, err := r.pool.Exec(ctx, attachQuery, att.ID, att.MessageID, att.URL, att.Filename, att.ContentType, att.Size, att.Width, att.Height, att.CreatedAt)
			if err != nil {
				return err
			}
		}
	}

	if len(msg.Mentions) > 0 {
		mentionQuery := `INSERT INTO dm_message_mentions (message_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`
		for _, userID := range msg.Mentions {
			if _, err := r.pool.Exec(ctx, mentionQuery, msg.ID, userID); err != nil {
				return err
			}
		}
	}

	if msg.ReplyToID != nil {
		_, err = r.pool.Exec(ctx, `UPDATE dm_messages SET reply_count = reply_count + 1 WHERE id = $1`, *msg.ReplyToID)
		if err != nil {
			return err
		}
	}

	_, err = r.pool.Exec(ctx, `UPDATE dm_channels SET updated_at = NOW() WHERE id = $1`, msg.ChannelID)
	return err
}

func (r *MessageRepository) GetByID(ctx context.Context, id int64) (*DMMessage, error) {
	query := `
		SELECT m.id, m.channel_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
		       m.reply_to_id, m.reply_count,
		       COALESCE((SELECT true FROM dm_pinned_messages WHERE message_id = m.id), false) as pinned
		FROM dm_messages m
		WHERE m.id = $1
	`
	msg := &DMMessage{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&msg.ID, &msg.ChannelID, &msg.AuthorID, &msg.Content,
		&msg.CreatedAt, &msg.EditedAt, &msg.DeletedAt,
		&msg.ReplyToID, &msg.ReplyCount, &msg.Pinned,
	)
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

func (r *MessageRepository) Update(ctx context.Context, msg *DMMessage) error {
	now := time.Now()
	msg.EditedAt = &now

	query := `
		UPDATE dm_messages
		SET content = $2, edited_at = $3
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query, msg.ID, msg.Content, msg.EditedAt)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("message not found")
	}

	return nil
}

func (r *MessageRepository) SoftDelete(ctx context.Context, id int64) error {
	query := `
		UPDATE dm_messages
		SET deleted_at = $2
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query, id, time.Now())
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("message not found")
	}

	return nil
}

func (r *MessageRepository) ListByChannel(ctx context.Context, channelID uuid.UUID, beforeID, afterID *int64, limit int) ([]*DMMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var query string
	var args []interface{}

	if beforeID != nil {
		query = `
			SELECT m.id, m.channel_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
			       m.reply_to_id, m.reply_count,
			       COALESCE((SELECT true FROM dm_pinned_messages WHERE message_id = m.id), false) as pinned
			FROM dm_messages m
			WHERE m.channel_id = $1 AND m.id < $2 AND m.deleted_at IS NULL
			ORDER BY m.id DESC LIMIT $3
		`
		args = []interface{}{channelID, *beforeID, limit}
	} else if afterID != nil {
		query = `
			SELECT m.id, m.channel_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
			       m.reply_to_id, m.reply_count,
			       COALESCE((SELECT true FROM dm_pinned_messages WHERE message_id = m.id), false) as pinned
			FROM dm_messages m
			WHERE m.channel_id = $1 AND m.id > $2 AND m.deleted_at IS NULL
			ORDER BY m.id ASC LIMIT $3
		`
		args = []interface{}{channelID, *afterID, limit}
	} else {
		query = `
			SELECT m.id, m.channel_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
			       m.reply_to_id, m.reply_count,
			       COALESCE((SELECT true FROM dm_pinned_messages WHERE message_id = m.id), false) as pinned
			FROM dm_messages m
			WHERE m.channel_id = $1 AND m.deleted_at IS NULL
			ORDER BY m.id DESC LIMIT $2
		`
		args = []interface{}{channelID, limit}
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*DMMessage
	var messageIDs []int64
	for rows.Next() {
		msg := &DMMessage{}
		if err := rows.Scan(
			&msg.ID, &msg.ChannelID, &msg.AuthorID, &msg.Content,
			&msg.CreatedAt, &msg.EditedAt, &msg.DeletedAt,
			&msg.ReplyToID, &msg.ReplyCount, &msg.Pinned,
		); err != nil {
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

	if beforeID == nil && afterID == nil {
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
	}

	return messages, rows.Err()
}

func (r *MessageRepository) GetAttachmentsBatch(ctx context.Context, messageIDs []int64) (map[int64][]DMAttachment, error) {
	if len(messageIDs) == 0 {
		return make(map[int64][]DMAttachment), nil
	}

	query := `
		SELECT id, message_id, url, filename, content_type, size, width, height, created_at
		FROM dm_message_attachments
		WHERE message_id = ANY($1)
		ORDER BY created_at ASC
	`

	rows, err := r.pool.Query(ctx, query, messageIDs)
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

func (r *MessageRepository) GetReactionsBatch(ctx context.Context, messageIDs []int64) (map[int64][]DMReaction, error) {
	if len(messageIDs) == 0 {
		return make(map[int64][]DMReaction), nil
	}

	query := `
		SELECT id, message_id, user_id, emoji, created_at
		FROM dm_message_reactions
		WHERE message_id = ANY($1)
		ORDER BY created_at ASC
	`

	rows, err := r.pool.Query(ctx, query, messageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64][]DMReaction)
	for rows.Next() {
		var r DMReaction
		if err := rows.Scan(&r.ID, &r.MessageID, &r.UserID, &r.Emoji, &r.CreatedAt); err != nil {
			return nil, err
		}
		result[r.MessageID] = append(result[r.MessageID], r)
	}

	return result, rows.Err()
}

func (r *MessageRepository) GetMentionsBatch(ctx context.Context, messageIDs []int64) (map[int64][]uuid.UUID, error) {
	if len(messageIDs) == 0 {
		return make(map[int64][]uuid.UUID), nil
	}

	query := `SELECT message_id, user_id FROM dm_message_mentions WHERE message_id = ANY($1)`

	rows, err := r.pool.Query(ctx, query, messageIDs)
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

func (r *MessageRepository) GetMentions(ctx context.Context, messageID int64) ([]uuid.UUID, error) {
	query := `SELECT user_id FROM dm_message_mentions WHERE message_id = $1`
	rows, err := r.pool.Query(ctx, query, messageID)
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

func (r *MessageRepository) AddReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (*DMReaction, error) {
	reaction := &DMReaction{
		ID:        uuid.New(),
		MessageID: messageID,
		UserID:    userID,
		Emoji:     emoji,
		CreatedAt: time.Now(),
	}

	query := `
		INSERT INTO dm_message_reactions (id, message_id, user_id, emoji, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (message_id, user_id, emoji) DO NOTHING
		RETURNING created_at
	`

	err := r.pool.QueryRow(ctx, query, reaction.ID, messageID, userID, emoji, reaction.CreatedAt).Scan(&reaction.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, errors.Conflict("reaction already exists")
	}
	if err != nil {
		return nil, err
	}

	return reaction, nil
}

func (r *MessageRepository) RemoveReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (uuid.UUID, error) {
	query := `
		DELETE FROM dm_message_reactions 
		WHERE message_id = $1 AND user_id = $2 AND emoji = $3
		RETURNING id
	`

	var reactionID uuid.UUID
	err := r.pool.QueryRow(ctx, query, messageID, userID, emoji).Scan(&reactionID)
	if err == pgx.ErrNoRows {
		return uuid.Nil, errors.NotFound("reaction not found")
	}
	if err != nil {
		return uuid.Nil, err
	}

	return reactionID, nil
}

func (r *MessageRepository) PinMessage(ctx context.Context, channelID uuid.UUID, messageID int64, pinnedBy uuid.UUID) error {
	query := `
		INSERT INTO dm_pinned_messages (channel_id, message_id, pinned_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (channel_id, message_id) DO NOTHING
	`
	_, err := r.pool.Exec(ctx, query, channelID, messageID, pinnedBy)
	return err
}

func (r *MessageRepository) UnpinMessage(ctx context.Context, channelID uuid.UUID, messageID int64) error {
	query := `DELETE FROM dm_pinned_messages WHERE channel_id = $1 AND message_id = $2`
	result, err := r.pool.Exec(ctx, query, channelID, messageID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("pinned message not found")
	}
	return nil
}

func (r *MessageRepository) ListPinnedMessages(ctx context.Context, channelID uuid.UUID) ([]*DMMessage, error) {
	query := `
		SELECT m.id, m.channel_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
		       m.reply_to_id, m.reply_count, true as pinned
		FROM dm_messages m
		INNER JOIN dm_pinned_messages pm ON m.id = pm.message_id
		WHERE pm.channel_id = $1 AND m.deleted_at IS NULL
		ORDER BY pm.pinned_at DESC
	`

	rows, err := r.pool.Query(ctx, query, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*DMMessage
	var messageIDs []int64
	for rows.Next() {
		msg := &DMMessage{}
		if err := rows.Scan(
			&msg.ID, &msg.ChannelID, &msg.AuthorID, &msg.Content,
			&msg.CreatedAt, &msg.EditedAt, &msg.DeletedAt,
			&msg.ReplyToID, &msg.ReplyCount, &msg.Pinned,
		); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
		messageIDs = append(messageIDs, msg.ID)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(messageIDs) > 0 {
		attachmentsMap, _ := r.GetAttachmentsBatch(ctx, messageIDs)
		reactionsMap, _ := r.GetReactionsBatch(ctx, messageIDs)

		for _, msg := range messages {
			msg.Attachments = attachmentsMap[msg.ID]
			msg.Reactions = reactionsMap[msg.ID]
		}
	}

	return messages, nil
}

func (r *MessageRepository) GetThreadReplies(ctx context.Context, parentID int64, limit, offset int) ([]*DMMessage, error) {
	query := `
		SELECT m.id, m.channel_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
		       m.reply_to_id, m.reply_count,
		       COALESCE((SELECT true FROM dm_pinned_messages WHERE message_id = m.id), false) as pinned
		FROM dm_messages m
		WHERE m.reply_to_id = $1 AND m.deleted_at IS NULL
		ORDER BY m.id ASC
		LIMIT $2 OFFSET $3
	`

	rows, err := r.pool.Query(ctx, query, parentID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*DMMessage
	var messageIDs []int64
	for rows.Next() {
		msg := &DMMessage{}
		if err := rows.Scan(
			&msg.ID, &msg.ChannelID, &msg.AuthorID, &msg.Content,
			&msg.CreatedAt, &msg.EditedAt, &msg.DeletedAt,
			&msg.ReplyToID, &msg.ReplyCount, &msg.Pinned,
		); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
		messageIDs = append(messageIDs, msg.ID)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(messageIDs) > 0 {
		attachmentsMap, _ := r.GetAttachmentsBatch(ctx, messageIDs)
		reactionsMap, _ := r.GetReactionsBatch(ctx, messageIDs)

		for _, msg := range messages {
			msg.Attachments = attachmentsMap[msg.ID]
			msg.Reactions = reactionsMap[msg.ID]
		}
	}

	return messages, nil
}

func (r *MessageRepository) Search(ctx context.Context, channelID uuid.UUID, query string, limit int) ([]*DMMessage, error) {
	sqlQuery := `
		SELECT m.id, m.channel_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
		       m.reply_to_id, m.reply_count,
		       COALESCE((SELECT true FROM dm_pinned_messages WHERE message_id = m.id), false) as pinned
		FROM dm_messages m
		WHERE m.channel_id = $1 
		AND m.deleted_at IS NULL
		AND m.content ILIKE '%' || $2 || '%'
		ORDER BY m.created_at DESC
		LIMIT $3
	`

	rows, err := r.pool.Query(ctx, sqlQuery, channelID, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*DMMessage
	var messageIDs []int64
	for rows.Next() {
		msg := &DMMessage{}
		if err := rows.Scan(
			&msg.ID, &msg.ChannelID, &msg.AuthorID, &msg.Content,
			&msg.CreatedAt, &msg.EditedAt, &msg.DeletedAt,
			&msg.ReplyToID, &msg.ReplyCount, &msg.Pinned,
		); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
		messageIDs = append(messageIDs, msg.ID)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(messageIDs) > 0 {
		attachmentsMap, _ := r.GetAttachmentsBatch(ctx, messageIDs)
		reactionsMap, _ := r.GetReactionsBatch(ctx, messageIDs)

		for _, msg := range messages {
			msg.Attachments = attachmentsMap[msg.ID]
			msg.Reactions = reactionsMap[msg.ID]
		}
	}

	return messages, nil
}

func (r *MessageRepository) GetAttachments(ctx context.Context, messageID int64) ([]DMAttachment, error) {
	query := `
		SELECT id, message_id, url, filename, content_type, size, width, height, created_at
		FROM dm_message_attachments WHERE message_id = $1 ORDER BY created_at
	`
	rows, err := r.pool.Query(ctx, query, messageID)
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

func (r *MessageRepository) GetReactions(ctx context.Context, messageID int64) ([]DMReaction, error) {
	query := `
		SELECT id, message_id, user_id, emoji, created_at
		FROM dm_message_reactions WHERE message_id = $1 ORDER BY created_at
	`
	rows, err := r.pool.Query(ctx, query, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reactions []DMReaction
	for rows.Next() {
		var r DMReaction
		if err := rows.Scan(&r.ID, &r.MessageID, &r.UserID, &r.Emoji, &r.CreatedAt); err != nil {
			return nil, err
		}
		reactions = append(reactions, r)
	}
	return reactions, rows.Err()
}
