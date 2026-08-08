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

// User is the persisted user record. Status stores the user's chosen preference;
// StatusPreference and the effective Status are derived at the service layer.
// PasswordHash and the OAuth fields are nil for accounts lacking that credential,
// and a non-nil DeletedAt marks a soft-deleted account excluded from queries.
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
	StatusPreference   string
}

// UserAvatar is one entry in a user's avatar history, referencing the stored full
// and thumbnail images.
type UserAvatar struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	FullURL          string
	ThumbnailURL     string
	OriginalFilename string
	SizeBytes        int64
	CreatedAt        time.Time
}

// Repository provides persistence for users and their avatar history. When cache
// is non-nil, reads are served from and writes populate/invalidate an optional
// cache layer; a nil cache disables all caching.
type Repository struct {
	pool  *pgxpool.Pool
	cache *cache.Cache
}

// NewRepository returns a Repository backed by pool with caching disabled.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// NewRepositoryWithCache returns a Repository backed by pool that also reads
// through and invalidates the given cache.
func NewRepositoryWithCache(pool *pgxpool.Pool, c *cache.Cache) *Repository {
	return &Repository{pool: pool, cache: c}
}

const (
	// userCacheTTL is how long a cached full user record stays valid.
	userCacheTTL = 5 * time.Minute
	// userByHandleTTL is how long a cached handle→ID mapping stays valid.
	userByHandleTTL = 5 * time.Minute
	// userStatusCacheTTL is how long a cached user status stays valid (kept short since status changes often).
	userStatusCacheTTL = 1 * time.Minute
)

// userCacheKey returns the cache key for a full user record by ID.
func (r *Repository) userCacheKey(id uuid.UUID) string {
	return fmt.Sprintf("user:%s", id.String())
}

// userHandleCacheKey returns the cache key mapping a handle to a user ID.
func (r *Repository) userHandleCacheKey(handle string) string {
	return fmt.Sprintf("user:handle:%s", handle)
}

// userStatusCacheKey returns the cache key for a user's cached status.
func (r *Repository) userStatusCacheKey(id uuid.UUID) string {
	return fmt.Sprintf("user:status:%s", id.String())
}

// userFullColumns is the SELECT column list matching scanFullUser's scan order.
const userFullColumns = `id, handle, display_name, avatar_url, avatar_thumbnail_url, bio, status, password_hash, oauth_provider, oauth_subject, created_at, deleted_at`

// scanFullUser scans a full user row (in userFullColumns order), mapping
// pgx.ErrNoRows to a NotFound error.
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

// Create inserts a new user, assigning a random ID if unset and populating
// CreatedAt from the DB. On success it warms the user and handle caches.
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

// GetByID returns the non-deleted user with the given ID, reading through the
// cache when enabled and caching the result on a miss. Returns NotFound if absent.
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

// GetByHandle returns the non-deleted user with the given handle. A cache hit on
// the handle→ID mapping is resolved via GetByID; on a miss it queries and warms
// both the user and handle caches. Returns NotFound if absent.
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

// GetByOAuth returns the non-deleted user matching the OAuth provider/subject
// pair, caching the result by ID. Returns NotFound if absent.
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

// Update writes the user's display name, avatar URLs, and bio, and invalidates the
// cached user record. Returns NotFound if the user is missing or soft-deleted.
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

// UpdatePasswordByHandle sets the bcrypt password hash for the user with the given
// handle and invalidates their cached record (which carries the hash), so a running
// server picks up the change immediately rather than after TTL. Returns false if no
// such user exists.
func (r *Repository) UpdatePasswordByHandle(ctx context.Context, handle, passwordHash string) (bool, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx,
		`UPDATE users SET password_hash = $2 WHERE handle = $1 RETURNING id`, handle, passwordHash,
	).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.cache != nil {
		_ = r.cache.Delete(ctx, r.userCacheKey(id))
	}
	return true, nil
}

// UpdateAvatar sets the user's current avatar and thumbnail URLs and invalidates
// the cached user record. Returns NotFound if the user is missing or soft-deleted.
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

// UpdateStatus persists the user's status preference, refreshes the cached status
// entry, and invalidates the cached full user record. Returns NotFound if the user
// is missing or soft-deleted.
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

// GetStatus returns the user's stored status, reading through the short-lived
// status cache when enabled. Returns NotFound if the user is missing or soft-deleted.
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

// GetMultipleByIDs returns the non-deleted users for the given IDs. With caching
// enabled it serves cache hits first and queries only the missing IDs, warming the
// cache for each fetched user; order is not guaranteed and missing users are omitted.
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

// Search returns up to limit non-deleted users whose handle or display name
// matches query (case-insensitive substring), ranked with exact and prefix handle
// matches first.
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

// ListByIDs returns the non-deleted users for the given IDs (including bio),
// bypassing the cache; returns nil when ids is empty.
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

// InvalidateCache evicts the user's cached record, handle mapping, and status. It
// looks up the user to learn the handle key; if that lookup fails it still deletes
// the ID-keyed record. A no-op when caching is disabled.
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

// CreateUserAvatar inserts a new avatar history entry, assigning a random ID if
// unset and populating CreatedAt from the DB.
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

// GetUserAvatar returns the avatar history entry by ID, or NotFound if absent.
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

// ListUserAvatars returns a user's avatar history newest-first; limit is clamped
// to the range (0, MaxAvatarHistory].
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

// DeleteUserAvatar removes an avatar history entry by ID, returning NotFound if it
// did not exist.
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

// CountUserAvatars returns how many avatar history entries the user has, used to
// enforce MaxAvatarHistory.
func (r *Repository) CountUserAvatars(ctx context.Context, userID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_avatars WHERE user_id = $1`, userID).Scan(&count)
	return count, err
}

// GetOldestUserAvatar returns the user's oldest avatar entry (the pruning
// candidate), or nil, nil if they have none.
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

// GetLatestUserAvatar returns the user's most recent avatar entry (used to
// fall back to after deletion), or nil, nil if they have none.
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

// GetStatusPreference reads the user's stored status and normalizes it to one of
// StatusDND, StatusOffline, or StatusOnline (the default for any other value).
func (r *Repository) GetStatusPreference(ctx context.Context, userID uuid.UUID) (string, error) {
	var status string

	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(status, 'online') FROM users WHERE id = $1`,
		userID,
	).Scan(&status)
	if err != nil {
		return "", err
	}

	switch status {
	case StatusDND:
		return StatusDND, nil
	case StatusOffline:
		return StatusOffline, nil
	default:
		return StatusOnline, nil
	}
}
