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
	ID        int64
	RoomID    uuid.UUID
	AuthorID  uuid.UUID
	Content   string
	CreatedAt time.Time
	EditedAt  *time.Time
	DeletedAt *time.Time
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
		INSERT INTO messages (id, room_id, author_id, content, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`

	msg.CreatedAt = r.snowflake.ExtractTimestamp(msg.ID)

	_, err := r.pool.Exec(ctx, query,
		msg.ID,
		msg.RoomID,
		msg.AuthorID,
		msg.Content,
		msg.CreatedAt,
	)

	return err
}

func (r *Repository) GetByID(ctx context.Context, id int64) (*Message, error) {
	query := `
		SELECT id, room_id, author_id, content, created_at, edited_at, deleted_at
		FROM messages
		WHERE id = $1
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
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("message not found")
	}
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
			SELECT id, room_id, author_id, content, created_at, edited_at, deleted_at
			FROM messages
			WHERE room_id = $1 AND id < $2 AND deleted_at IS NULL
			ORDER BY id DESC
			LIMIT $3
		`
		args = []interface{}{roomID, *beforeID, limit}
	} else if afterID != nil {
		query = `
			SELECT id, room_id, author_id, content, created_at, edited_at, deleted_at
			FROM messages
			WHERE room_id = $1 AND id > $2 AND deleted_at IS NULL
			ORDER BY id ASC
			LIMIT $3
		`
		args = []interface{}{roomID, *afterID, limit}
	} else {
		query = `
			SELECT id, room_id, author_id, content, created_at, edited_at, deleted_at
			FROM messages
			WHERE room_id = $1 AND deleted_at IS NULL
			ORDER BY id DESC
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
		); err != nil {
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

func timePtr(t time.Time) *time.Time {
	return &t
}
