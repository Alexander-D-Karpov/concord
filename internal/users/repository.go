package users

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID            uuid.UUID
	Handle        string
	DisplayName   string
	AvatarURL     string
	OAuthProvider *string
	OAuthSubject  *string
	PasswordHash  *string
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, user *User) error {
	query := `
		INSERT INTO users (id, handle, display_name, avatar_url, oauth_provider, oauth_subject, password_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at
	`

	if user.ID == uuid.Nil {
		user.ID = uuid.New()
	}

	err := r.pool.QueryRow(ctx, query,
		user.ID,
		user.Handle,
		user.DisplayName,
		user.AvatarURL,
		user.OAuthProvider,
		user.OAuthSubject,
		user.PasswordHash,
	).Scan(&user.CreatedAt)

	if err != nil {
		return err
	}

	return nil
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	query := `
		SELECT id, handle, display_name, avatar_url, oauth_provider, oauth_subject, password_hash, created_at, deleted_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&user.ID,
		&user.Handle,
		&user.DisplayName,
		&user.AvatarURL,
		&user.OAuthProvider,
		&user.OAuthSubject,
		&user.PasswordHash,
		&user.CreatedAt,
		&user.DeletedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("user not found")
	}
	if err != nil {
		return nil, err
	}

	return user, nil
}

func (r *Repository) GetByHandle(ctx context.Context, handle string) (*User, error) {
	query := `
		SELECT id, handle, display_name, avatar_url, oauth_provider, oauth_subject, password_hash, created_at, deleted_at
		FROM users
		WHERE handle = $1 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.pool.QueryRow(ctx, query, handle).Scan(
		&user.ID,
		&user.Handle,
		&user.DisplayName,
		&user.AvatarURL,
		&user.OAuthProvider,
		&user.OAuthSubject,
		&user.PasswordHash,
		&user.CreatedAt,
		&user.DeletedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("user not found")
	}
	if err != nil {
		return nil, err
	}

	return user, nil
}

func (r *Repository) GetByOAuth(ctx context.Context, provider, subject string) (*User, error) {
	query := `
		SELECT id, handle, display_name, avatar_url, oauth_provider, oauth_subject, password_hash, created_at, deleted_at
		FROM users
		WHERE oauth_provider = $1 AND oauth_subject = $2 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.pool.QueryRow(ctx, query, provider, subject).Scan(
		&user.ID,
		&user.Handle,
		&user.DisplayName,
		&user.AvatarURL,
		&user.OAuthProvider,
		&user.OAuthSubject,
		&user.PasswordHash,
		&user.CreatedAt,
		&user.DeletedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("user not found")
	}
	if err != nil {
		return nil, err
	}

	return user, nil
}

func (r *Repository) Update(ctx context.Context, user *User) error {
	query := `
		UPDATE users
		SET display_name = $2, avatar_url = $3
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query, user.ID, user.DisplayName, user.AvatarURL)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("user not found")
	}

	return nil
}
