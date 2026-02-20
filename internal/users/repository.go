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
	ID                 uuid.UUID
	Handle             string
	DisplayName        string
	AvatarURL          string
	AvatarThumbnailURL string
	Bio                string
	Status             string
	PasswordHash       *string
	OAuthProvider      *string
	OAuthSubject       *string
	CreatedAt          time.Time
	DeletedAt          *time.Time
}

type UserAvatar struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	FullURL          string
	ThumbnailURL     string
	OriginalFilename string
	SizeBytes        int64
	CreatedAt        time.Time
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

const userFullColumns = `id, handle, display_name, avatar_url, avatar_thumbnail_url, bio, status, password_hash, oauth_provider, oauth_subject, created_at, deleted_at`

func scanFullUser(row pgx.Row) (*User, error) {
	user := &User{}
	err := row.Scan(
		&user.ID, &user.Handle, &user.DisplayName,
		&user.AvatarURL, &user.AvatarThumbnailURL,
		&user.Bio, &user.Status, &user.PasswordHash,
		&user.OAuthProvider, &user.OAuthSubject,
		&user.CreatedAt, &user.DeletedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("user not found")
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (r *Repository) Create(ctx context.Context, user *User) error {
	query := `
		INSERT INTO users (id, handle, display_name, avatar_url, avatar_thumbnail_url, bio, password_hash, oauth_provider, oauth_subject)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at
	`
	if user.ID == uuid.Nil {
		user.ID = uuid.New()
	}

	err := r.pool.QueryRow(ctx, query,
		user.ID, user.Handle, user.DisplayName,
		user.AvatarURL, user.AvatarThumbnailURL,
		user.Bio, user.PasswordHash,
		user.OAuthProvider, user.OAuthSubject,
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

	query := fmt.Sprintf(`SELECT %s FROM users WHERE id = $1 AND deleted_at IS NULL`, userFullColumns)
	user, err := scanFullUser(r.pool.QueryRow(ctx, query, id))
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

	query := fmt.Sprintf(`SELECT %s FROM users WHERE handle = $1 AND deleted_at IS NULL`, userFullColumns)
	user, err := scanFullUser(r.pool.QueryRow(ctx, query, handle))
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
	query := fmt.Sprintf(`SELECT %s FROM users WHERE oauth_provider = $1 AND oauth_subject = $2 AND deleted_at IS NULL`, userFullColumns)
	user, err := scanFullUser(r.pool.QueryRow(ctx, query, provider, subject))
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.userCacheKey(user.ID), user, userCacheTTL)
	}
	return user, nil
}

func (r *Repository) Update(ctx context.Context, user *User) error {
	query := `UPDATE users SET display_name = $2, avatar_url = $3, avatar_thumbnail_url = $4, bio = $5 WHERE id = $1 AND deleted_at IS NULL`

	result, err := r.pool.Exec(ctx, query,
		user.ID, user.DisplayName, user.AvatarURL, user.AvatarThumbnailURL, user.Bio,
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

func (r *Repository) UpdateAvatar(ctx context.Context, id uuid.UUID, avatarURL, thumbnailURL string) error {
	query := `UPDATE users SET avatar_url = $2, avatar_thumbnail_url = $3 WHERE id = $1 AND deleted_at IS NULL`
	result, err := r.pool.Exec(ctx, query, id, avatarURL, thumbnailURL)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("user not found")
	}
	if r.cache != nil {
		_ = r.cache.Delete(ctx, r.userCacheKey(id))
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

	query := fmt.Sprintf(`SELECT %s FROM users WHERE id = ANY($1) AND deleted_at IS NULL`, userFullColumns)
	rows, err := r.pool.Query(ctx, query, missingIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		user := &User{}
		if err := rows.Scan(
			&user.ID, &user.Handle, &user.DisplayName,
			&user.AvatarURL, &user.AvatarThumbnailURL,
			&user.Bio, &user.Status, &user.PasswordHash,
			&user.OAuthProvider, &user.OAuthSubject,
			&user.CreatedAt, &user.DeletedAt,
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
		SELECT id, handle, display_name, avatar_url, avatar_thumbnail_url, status, created_at
		FROM users
		WHERE deleted_at IS NULL
		AND (handle ILIKE '%' || $1 || '%' OR display_name ILIKE '%' || $1 || '%')
		ORDER BY
			CASE WHEN handle = $1 THEN 0
			     WHEN handle ILIKE $1 || '%' THEN 1
			     ELSE 2
			END, handle
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
			&user.ID, &user.Handle, &user.DisplayName,
			&user.AvatarURL, &user.AvatarThumbnailURL,
			&user.Status, &user.CreatedAt,
		); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (r *Repository) ListByIDs(ctx context.Context, ids []uuid.UUID) ([]*User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query := `
		SELECT id, handle, display_name, avatar_url, avatar_thumbnail_url, status, bio, created_at
		FROM users WHERE id = ANY($1) AND deleted_at IS NULL
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
			&user.ID, &user.Handle, &user.DisplayName,
			&user.AvatarURL, &user.AvatarThumbnailURL,
			&user.Status, &user.Bio, &user.CreatedAt,
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

// --- Avatar History ---

func (r *Repository) CreateUserAvatar(ctx context.Context, av *UserAvatar) error {
	if av.ID == uuid.Nil {
		av.ID = uuid.New()
	}
	query := `
		INSERT INTO user_avatars (id, user_id, full_url, thumbnail_url, original_filename, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at
	`
	return r.pool.QueryRow(ctx, query,
		av.ID, av.UserID, av.FullURL, av.ThumbnailURL, av.OriginalFilename, av.SizeBytes,
	).Scan(&av.CreatedAt)
}

func (r *Repository) GetUserAvatar(ctx context.Context, id uuid.UUID) (*UserAvatar, error) {
	query := `SELECT id, user_id, full_url, thumbnail_url, original_filename, size_bytes, created_at FROM user_avatars WHERE id = $1`
	av := &UserAvatar{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&av.ID, &av.UserID, &av.FullURL, &av.ThumbnailURL,
		&av.OriginalFilename, &av.SizeBytes, &av.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("avatar not found")
	}
	return av, err
}

func (r *Repository) ListUserAvatars(ctx context.Context, userID uuid.UUID, limit int) ([]*UserAvatar, error) {
	if limit <= 0 || limit > MaxAvatarHistory {
		limit = MaxAvatarHistory
	}
	query := `SELECT id, user_id, full_url, thumbnail_url, original_filename, size_bytes, created_at
		FROM user_avatars WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`
	rows, err := r.pool.Query(ctx, query, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var avatars []*UserAvatar
	for rows.Next() {
		av := &UserAvatar{}
		if err := rows.Scan(&av.ID, &av.UserID, &av.FullURL, &av.ThumbnailURL,
			&av.OriginalFilename, &av.SizeBytes, &av.CreatedAt); err != nil {
			return nil, err
		}
		avatars = append(avatars, av)
	}
	return avatars, rows.Err()
}

func (r *Repository) DeleteUserAvatar(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM user_avatars WHERE id = $1`
	result, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("avatar not found")
	}
	return nil
}

func (r *Repository) CountUserAvatars(ctx context.Context, userID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_avatars WHERE user_id = $1`, userID).Scan(&count)
	return count, err
}

func (r *Repository) GetOldestUserAvatar(ctx context.Context, userID uuid.UUID) (*UserAvatar, error) {
	query := `SELECT id, user_id, full_url, thumbnail_url, original_filename, size_bytes, created_at
		FROM user_avatars WHERE user_id = $1 ORDER BY created_at ASC LIMIT 1`
	av := &UserAvatar{}
	err := r.pool.QueryRow(ctx, query, userID).Scan(
		&av.ID, &av.UserID, &av.FullURL, &av.ThumbnailURL,
		&av.OriginalFilename, &av.SizeBytes, &av.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return av, err
}

func (r *Repository) GetLatestUserAvatar(ctx context.Context, userID uuid.UUID) (*UserAvatar, error) {
	query := `SELECT id, user_id, full_url, thumbnail_url, original_filename, size_bytes, created_at
		FROM user_avatars WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`
	av := &UserAvatar{}
	err := r.pool.QueryRow(ctx, query, userID).Scan(
		&av.ID, &av.UserID, &av.FullURL, &av.ThumbnailURL,
		&av.OriginalFilename, &av.SizeBytes, &av.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return av, err
}
