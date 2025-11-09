package users

import (
	"context"
	"fmt"
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
	Status        string
	Bio           *string
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

func (r *Repository) Pool() *pgxpool.Pool {
	return r.pool
}

func (r *Repository) Create(ctx context.Context, user *User) error {
	query := `
		INSERT INTO users (id, handle, display_name, avatar_url, oauth_provider, oauth_subject, password_hash, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at
	`

	if user.ID == uuid.Nil {
		user.ID = uuid.New()
	}

	if user.Status == "" {
		user.Status = "offline"
	}

	err := r.pool.QueryRow(ctx, query,
		user.ID,
		user.Handle,
		user.DisplayName,
		user.AvatarURL,
		user.OAuthProvider,
		user.OAuthSubject,
		user.PasswordHash,
		user.Status,
	).Scan(&user.CreatedAt)

	return err
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	query := `
		SELECT id, handle, display_name, avatar_url, status, bio, oauth_provider, oauth_subject, password_hash, created_at, deleted_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&user.ID,
		&user.Handle,
		&user.DisplayName,
		&user.AvatarURL,
		&user.Status,
		&user.Bio,
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
		SELECT id, handle, display_name, avatar_url, status, bio, oauth_provider, oauth_subject, password_hash, created_at, deleted_at
		FROM users
		WHERE handle = $1 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.pool.QueryRow(ctx, query, handle).Scan(
		&user.ID,
		&user.Handle,
		&user.DisplayName,
		&user.AvatarURL,
		&user.Status,
		&user.Bio,
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
		SELECT id, handle, display_name, avatar_url, status, bio, oauth_provider, oauth_subject, password_hash, created_at, deleted_at
		FROM users
		WHERE oauth_provider = $1 AND oauth_subject = $2 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.pool.QueryRow(ctx, query, provider, subject).Scan(
		&user.ID,
		&user.Handle,
		&user.DisplayName,
		&user.AvatarURL,
		&user.Status,
		&user.Bio,
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
		SET display_name = $2, avatar_url = $3, bio = $4
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query, user.ID, user.DisplayName, user.AvatarURL, user.Bio)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("user not found")
	}

	return nil
}

func (r *Repository) Search(ctx context.Context, query string, excludeUserID *uuid.UUID, limit, offset int) ([]*User, error) {
	sqlQuery := `
		SELECT id, handle, display_name, avatar_url, status, bio, created_at
		FROM users
		WHERE deleted_at IS NULL 
		AND (handle ILIKE $1 OR display_name ILIKE $1)
	`
	args := []interface{}{"%" + query + "%"}

	if excludeUserID != nil {
		sqlQuery += " AND id != $" + fmt.Sprint(len(args)+1)
		args = append(args, *excludeUserID)
	}

	sqlQuery += `
		ORDER BY 
			CASE 
				WHEN handle ILIKE $` + fmt.Sprint(len(args)+1) + ` THEN 1
				WHEN display_name ILIKE $` + fmt.Sprint(len(args)+1) + ` THEN 2
				ELSE 3
			END,
			display_name ASC
		LIMIT $` + fmt.Sprint(len(args)+2) + ` OFFSET $` + fmt.Sprint(len(args)+3)

	args = append(args, query+"%", limit, offset)

	rows, err := r.pool.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		user := &User{}
		if err := rows.Scan(
			&user.ID,
			&user.Handle,
			&user.DisplayName,
			&user.AvatarURL,
			&user.Status,
			&user.Bio,
			&user.CreatedAt,
		); err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return users, rows.Err()
}

func (r *Repository) ListByIDs(ctx context.Context, ids []uuid.UUID) ([]*User, error) {
	if len(ids) == 0 {
		return []*User{}, nil
	}

	query := `
		SELECT id, handle, display_name, avatar_url, status, bio, created_at
		FROM users
		WHERE id = ANY($1) AND deleted_at IS NULL
		ORDER BY display_name ASC
	`

	rows, err := r.pool.Query(ctx, query, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		user := &User{}
		if err := rows.Scan(
			&user.ID,
			&user.Handle,
			&user.DisplayName,
			&user.AvatarURL,
			&user.Status,
			&user.Bio,
			&user.CreatedAt,
		); err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return users, rows.Err()
}

func (r *Repository) UpdateStatus(ctx context.Context, userID uuid.UUID, status string) error {
	query := `UPDATE users SET status = $2 WHERE id = $1 AND deleted_at IS NULL`
	_, err := r.pool.Exec(ctx, query, userID, status)
	return err
}
