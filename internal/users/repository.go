package users

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID            uuid.UUID
	Handle        string
	DisplayName   string
	AvatarURL     string
	Bio           string
	Status        string
	PasswordHash  *string
	OAuthProvider *string
	OAuthSubject  *string
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

type Repository struct {
	pool  *pgxpool.Pool
	cache *cache.Cache
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func NewRepositoryWithCache(pool *pgxpool.Pool, c *cache.Cache) *Repository {
	return &Repository{pool: pool, cache: c}
}

const (
	userCacheTTL       = 5 * time.Minute
	userByHandleTTL    = 5 * time.Minute
	userStatusCacheTTL = 1 * time.Minute
)

func (r *Repository) userCacheKey(id uuid.UUID) string {
	return fmt.Sprintf("user:%s", id.String())
}

func (r *Repository) userHandleCacheKey(handle string) string {
	return fmt.Sprintf("user:handle:%s", handle)
}

func (r *Repository) userStatusCacheKey(id uuid.UUID) string {
	return fmt.Sprintf("user:status:%s", id.String())
}

func (r *Repository) Create(ctx context.Context, user *User) error {
	query := `
		INSERT INTO users (id, handle, display_name, avatar_url, bio, password_hash, oauth_provider, oauth_subject)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
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
		user.Bio,
		user.PasswordHash,
		user.OAuthProvider,
		user.OAuthSubject,
	).Scan(&user.CreatedAt)

	if err != nil {
		return err
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.userCacheKey(user.ID), user, userCacheTTL)
		_ = r.cache.Set(ctx, r.userHandleCacheKey(user.Handle), user.ID, userByHandleTTL)
	}

	return nil
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	if r.cache != nil {
		var cached User
		if err := r.cache.Get(ctx, r.userCacheKey(id), &cached); err == nil {
			return &cached, nil
		}
	}

	query := `
		SELECT id, handle, display_name, avatar_url, bio, status, password_hash, 
		       oauth_provider, oauth_subject, created_at, deleted_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&user.ID,
		&user.Handle,
		&user.DisplayName,
		&user.AvatarURL,
		&user.Bio,
		&user.Status,
		&user.PasswordHash,
		&user.OAuthProvider,
		&user.OAuthSubject,
		&user.CreatedAt,
		&user.DeletedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("user not found")
	}
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.userCacheKey(id), user, userCacheTTL)
	}

	return user, nil
}

func (r *Repository) GetByHandle(ctx context.Context, handle string) (*User, error) {
	if r.cache != nil {
		var userID uuid.UUID
		if err := r.cache.Get(ctx, r.userHandleCacheKey(handle), &userID); err == nil {
			return r.GetByID(ctx, userID)
		}
	}

	query := `
		SELECT id, handle, display_name, avatar_url, bio, status, password_hash, 
		       oauth_provider, oauth_subject, created_at, deleted_at
		FROM users
		WHERE handle = $1 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.pool.QueryRow(ctx, query, handle).Scan(
		&user.ID,
		&user.Handle,
		&user.DisplayName,
		&user.AvatarURL,
		&user.Bio,
		&user.Status,
		&user.PasswordHash,
		&user.OAuthProvider,
		&user.OAuthSubject,
		&user.CreatedAt,
		&user.DeletedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("user not found")
	}
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.userCacheKey(user.ID), user, userCacheTTL)
		_ = r.cache.Set(ctx, r.userHandleCacheKey(handle), user.ID, userByHandleTTL)
	}

	return user, nil
}

func (r *Repository) GetByOAuth(ctx context.Context, provider, subject string) (*User, error) {
	query := `
		SELECT id, handle, display_name, avatar_url, bio, status, password_hash, 
		       oauth_provider, oauth_subject, created_at, deleted_at
		FROM users
		WHERE oauth_provider = $1 AND oauth_subject = $2 AND deleted_at IS NULL
	`

	user := &User{}
	err := r.pool.QueryRow(ctx, query, provider, subject).Scan(
		&user.ID,
		&user.Handle,
		&user.DisplayName,
		&user.AvatarURL,
		&user.Bio,
		&user.Status,
		&user.PasswordHash,
		&user.OAuthProvider,
		&user.OAuthSubject,
		&user.CreatedAt,
		&user.DeletedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("user not found")
	}
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.userCacheKey(user.ID), user, userCacheTTL)
	}

	return user, nil
}

func (r *Repository) Update(ctx context.Context, user *User) error {
	query := `
		UPDATE users
		SET display_name = $2, avatar_url = $3, bio = $4
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query,
		user.ID,
		user.DisplayName,
		user.AvatarURL,
		user.Bio,
	)

	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("user not found")
	}

	if r.cache != nil {
		_ = r.cache.Delete(ctx, r.userCacheKey(user.ID))
	}

	return nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	query := `UPDATE users SET status = $2 WHERE id = $1 AND deleted_at IS NULL`

	result, err := r.pool.Exec(ctx, query, id, status)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("user not found")
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.userStatusCacheKey(id), status, userStatusCacheTTL)
		_ = r.cache.Delete(ctx, r.userCacheKey(id))
	}

	return nil
}

func (r *Repository) GetStatus(ctx context.Context, id uuid.UUID) (string, error) {
	if r.cache != nil {
		var status string
		if err := r.cache.Get(ctx, r.userStatusCacheKey(id), &status); err == nil {
			return status, nil
		}
	}

	query := `SELECT status FROM users WHERE id = $1 AND deleted_at IS NULL`

	var status string
	err := r.pool.QueryRow(ctx, query, id).Scan(&status)
	if err == pgx.ErrNoRows {
		return "", errors.NotFound("user not found")
	}
	if err != nil {
		return "", err
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.userStatusCacheKey(id), status, userStatusCacheTTL)
	}

	return status, nil
}

func (r *Repository) GetMultipleByIDs(ctx context.Context, ids []uuid.UUID) ([]*User, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	users := make([]*User, 0, len(ids))
	missingIDs := make([]uuid.UUID, 0)

	if r.cache != nil {
		for _, id := range ids {
			var cached User
			if err := r.cache.Get(ctx, r.userCacheKey(id), &cached); err == nil {
				users = append(users, &cached)
			} else {
				missingIDs = append(missingIDs, id)
			}
		}

		if len(missingIDs) == 0 {
			return users, nil
		}
	} else {
		missingIDs = ids
	}

	query := `
		SELECT id, handle, display_name, avatar_url, bio, status, password_hash, 
		       oauth_provider, oauth_subject, created_at, deleted_at
		FROM users
		WHERE id = ANY($1) AND deleted_at IS NULL
	`

	rows, err := r.pool.Query(ctx, query, missingIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		user := &User{}
		if err := rows.Scan(
			&user.ID,
			&user.Handle,
			&user.DisplayName,
			&user.AvatarURL,
			&user.Bio,
			&user.Status,
			&user.PasswordHash,
			&user.OAuthProvider,
			&user.OAuthSubject,
			&user.CreatedAt,
			&user.DeletedAt,
		); err != nil {
			return nil, err
		}

		users = append(users, user)

		if r.cache != nil {
			_ = r.cache.Set(ctx, r.userCacheKey(user.ID), user, userCacheTTL)
		}
	}

	return users, rows.Err()
}

func (r *Repository) Search(ctx context.Context, query string, limit int) ([]*User, error) {
	sqlQuery := `
		SELECT id, handle, display_name, avatar_url, status, created_at
		FROM users
		WHERE deleted_at IS NULL
		AND (handle ILIKE '%' || $1 || '%' OR display_name ILIKE '%' || $1 || '%')
		ORDER BY 
			CASE WHEN handle = $1 THEN 0
			     WHEN handle ILIKE $1 || '%' THEN 1
			     ELSE 2
			END,
			handle
		LIMIT $2
	`

	rows, err := r.pool.Query(ctx, sqlQuery, query, limit)
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
			&user.CreatedAt,
		); err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return users, rows.Err()
}

func (r *Repository) InvalidateCache(ctx context.Context, userID uuid.UUID) error {
	if r.cache == nil {
		return nil
	}

	user, err := r.GetByID(ctx, userID)
	if err != nil {
		return r.cache.Delete(ctx, r.userCacheKey(userID))
	}

	return r.cache.Delete(ctx,
		r.userCacheKey(userID),
		r.userHandleCacheKey(user.Handle),
		r.userStatusCacheKey(userID),
	)
}

func (r *Repository) ListByIDs(ctx context.Context, ids []uuid.UUID) ([]*User, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	query := `
        SELECT id, handle, display_name, avatar_url, status, bio, created_at
        FROM users
        WHERE id = ANY($1) AND deleted_at IS NULL
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
