package messages

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Message struct {
	ID         int64
	RoomID     uuid.UUID
	AuthorID   uuid.UUID
	Content    string
	CreatedAt  time.Time
	EditedAt   *time.Time
	DeletedAt  *time.Time
	ReplyToID  *int64
	ReplyCount int32
	Pinned     bool
	Reactions  []Reaction
}

type Reaction struct {
	ID        uuid.UUID
	MessageID int64
	UserID    uuid.UUID
	Emoji     string
	CreatedAt time.Time
}

type Repository struct {
	pool      *pgxpool.Pool
	snowflake *infra.SnowflakeGenerator
}

func NewRepository(pool *pgxpool.Pool, snowflake *infra.SnowflakeGenerator) *Repository {
	return &Repository{
		pool:      pool,
		snowflake: snowflake,
	}
}

func (r *Repository) Create(ctx context.Context, msg *Message) error {
	if msg.ID == 0 {
		msg.ID = r.snowflake.Generate()
	}

	query := `
		INSERT INTO messages (id, room_id, author_id, content, reply_to_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`

	msg.CreatedAt = r.snowflake.ExtractTimestamp(msg.ID)

	_, err := r.pool.Exec(ctx, query,
		msg.ID,
		msg.RoomID,
		msg.AuthorID,
		msg.Content,
		msg.ReplyToID,
		msg.CreatedAt,
	)

	return err
}

func (r *Repository) GetByID(ctx context.Context, id int64) (*Message, error) {
	query := `
		SELECT m.id, m.room_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at, 
		       m.reply_to_id, m.reply_count,
		       COALESCE((SELECT true FROM pinned_messages WHERE message_id = m.id), false) as pinned
		FROM messages m
		WHERE m.id = $1
	`

	msg := &Message{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&msg.ID,
		&msg.RoomID,
		&msg.AuthorID,
		&msg.Content,
		&msg.CreatedAt,
		&msg.EditedAt,
		&msg.DeletedAt,
		&msg.ReplyToID,
		&msg.ReplyCount,
		&msg.Pinned,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("message not found")
	}
	if err != nil {
		return nil, err
	}

	msg.Reactions, err = r.GetReactions(ctx, id)
	if err != nil {
		return nil, err
	}

	return msg, nil
}

func (r *Repository) ListByRoom(ctx context.Context, roomID uuid.UUID, beforeID, afterID *int64, limit int) ([]*Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var query string
	var args []interface{}

	if beforeID != nil {
		query = `
			SELECT m.id, m.room_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
			       m.reply_to_id, m.reply_count,
			       COALESCE((SELECT true FROM pinned_messages WHERE message_id = m.id), false) as pinned
			FROM messages m
			WHERE m.room_id = $1 AND m.id < $2 AND m.deleted_at IS NULL
			ORDER BY m.id DESC
			LIMIT $3
		`
		args = []interface{}{roomID, *beforeID, limit}
	} else if afterID != nil {
		query = `
			SELECT m.id, m.room_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
			       m.reply_to_id, m.reply_count,
			       COALESCE((SELECT true FROM pinned_messages WHERE message_id = m.id), false) as pinned
			FROM messages m
			WHERE m.room_id = $1 AND m.id > $2 AND m.deleted_at IS NULL
			ORDER BY m.id ASC
			LIMIT $3
		`
		args = []interface{}{roomID, *afterID, limit}
	} else {
		query = `
			SELECT m.id, m.room_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
			       m.reply_to_id, m.reply_count,
			       COALESCE((SELECT true FROM pinned_messages WHERE message_id = m.id), false) as pinned
			FROM messages m
			WHERE m.room_id = $1 AND m.deleted_at IS NULL
			ORDER BY m.id DESC
			LIMIT $2
		`
		args = []interface{}{roomID, limit}
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		msg := &Message{}
		if err := rows.Scan(
			&msg.ID,
			&msg.RoomID,
			&msg.AuthorID,
			&msg.Content,
			&msg.CreatedAt,
			&msg.EditedAt,
			&msg.DeletedAt,
			&msg.ReplyToID,
			&msg.ReplyCount,
			&msg.Pinned,
		); err != nil {
			return nil, err
		}

		msg.Reactions, err = r.GetReactions(ctx, msg.ID)
		if err != nil {
			return nil, err
		}

		messages = append(messages, msg)
	}

	if afterID != nil {
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
	}

	return messages, rows.Err()
}

func (r *Repository) Update(ctx context.Context, msg *Message) error {
	query := `
		UPDATE messages
		SET content = $2, edited_at = $3
		WHERE id = $1 AND deleted_at IS NULL
	`

	msg.EditedAt = timePtr(time.Now())

	result, err := r.pool.Exec(ctx, query, msg.ID, msg.Content, msg.EditedAt)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("message not found")
	}

	return nil
}

func (r *Repository) SoftDelete(ctx context.Context, id int64) error {
	query := `
		UPDATE messages
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

func (r *Repository) AddReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (*Reaction, error) {
	reaction := &Reaction{
		ID:        uuid.New(),
		MessageID: messageID,
		UserID:    userID,
		Emoji:     emoji,
		CreatedAt: time.Now(),
	}

	query := `
		INSERT INTO message_reactions (id, message_id, user_id, emoji, created_at)
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

func (r *Repository) RemoveReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (uuid.UUID, error) {
	query := `
		DELETE FROM message_reactions 
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

func (r *Repository) GetReactions(ctx context.Context, messageID int64) ([]Reaction, error) {
	query := `
		SELECT id, message_id, user_id, emoji, created_at
		FROM message_reactions
		WHERE message_id = $1
		ORDER BY created_at ASC
	`

	rows, err := r.pool.Query(ctx, query, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reactions []Reaction
	for rows.Next() {
		var r Reaction
		if err := rows.Scan(&r.ID, &r.MessageID, &r.UserID, &r.Emoji, &r.CreatedAt); err != nil {
			return nil, err
		}
		reactions = append(reactions, r)
	}

	return reactions, rows.Err()
}

func (r *Repository) PinMessage(ctx context.Context, roomID uuid.UUID, messageID int64, pinnedBy uuid.UUID) error {
	query := `
		INSERT INTO pinned_messages (room_id, message_id, pinned_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (room_id, message_id) DO NOTHING
	`

	_, err := r.pool.Exec(ctx, query, roomID, messageID, pinnedBy)
	return err
}

func (r *Repository) UnpinMessage(ctx context.Context, roomID uuid.UUID, messageID int64) error {
	query := `
		DELETE FROM pinned_messages 
		WHERE room_id = $1 AND message_id = $2
	`

	result, err := r.pool.Exec(ctx, query, roomID, messageID)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("pinned message not found")
	}

	return nil
}

func (r *Repository) ListPinnedMessages(ctx context.Context, roomID uuid.UUID) ([]*Message, error) {
	query := `
		SELECT m.id, m.room_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
		       m.reply_to_id, m.reply_count, true as pinned
		FROM messages m
		INNER JOIN pinned_messages pm ON m.id = pm.message_id
		WHERE pm.room_id = $1 AND m.deleted_at IS NULL
		ORDER BY pm.pinned_at DESC
	`

	rows, err := r.pool.Query(ctx, query, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		msg := &Message{}
		if err := rows.Scan(
			&msg.ID,
			&msg.RoomID,
			&msg.AuthorID,
			&msg.Content,
			&msg.CreatedAt,
			&msg.EditedAt,
			&msg.DeletedAt,
			&msg.ReplyToID,
			&msg.ReplyCount,
			&msg.Pinned,
		); err != nil {
			return nil, err
		}

		msg.Reactions, err = r.GetReactions(ctx, msg.ID)
		if err != nil {
			return nil, err
		}

		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

func (r *Repository) IncrementReplyCount(ctx context.Context, messageID int64) error {
	query := `
		UPDATE messages 
		SET reply_count = reply_count + 1 
		WHERE id = $1
	`

	_, err := r.pool.Exec(ctx, query, messageID)
	return err
}

func (r *Repository) GetThreadReplies(ctx context.Context, parentID int64, limit, offset int) ([]*Message, error) {
	query := `
		SELECT m.id, m.room_id, m.author_id, m.content, m.created_at, m.edited_at, m.deleted_at,
		       m.reply_to_id, m.reply_count,
		       COALESCE((SELECT true FROM pinned_messages WHERE message_id = m.id), false) as pinned
		FROM messages m
		WHERE m.reply_to_id = $1 AND m.deleted_at IS NULL
		ORDER BY m.id ASC
		LIMIT $2 OFFSET $3
	`

	rows, err := r.pool.Query(ctx, query, parentID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		msg := &Message{}
		if err := rows.Scan(
			&msg.ID,
			&msg.RoomID,
			&msg.AuthorID,
			&msg.Content,
			&msg.CreatedAt,
			&msg.EditedAt,
			&msg.DeletedAt,
			&msg.ReplyToID,
			&msg.ReplyCount,
			&msg.Pinned,
		); err != nil {
			return nil, err
		}

		msg.Reactions, err = r.GetReactions(ctx, msg.ID)
		if err != nil {
			return nil, err
		}

		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

func timePtr(t time.Time) *time.Time {
	return &t
}
