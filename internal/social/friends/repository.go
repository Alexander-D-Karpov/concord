package friends

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

// FriendRequest is a stored friend request between two users; Status is one of
// "pending", "accepted", or "rejected".
type FriendRequest struct {
	ID         uuid.UUID
	FromUserID uuid.UUID
	ToUserID   uuid.UUID
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// FriendRequestWithUser is a FriendRequest enriched with both the sender's and
// recipient's profile fields for display.
type FriendRequestWithUser struct {
	ID          uuid.UUID
	FromUserID  uuid.UUID
	ToUserID    uuid.UUID
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	FromHandle  string
	FromDisplay string
	FromAvatar  string
	ToHandle    string
	ToDisplay   string
	ToAvatar    string
}

// Friend is a friend of some user, joining the friend's profile with the
// friendship's creation time; Status here holds the friend's stored status,
// decorated with live presence by the service layer.
type Friend struct {
	UserID             uuid.UUID
	Handle             string
	DisplayName        string
	AvatarURL          string
	AvatarThumbnailURL string
	Status             string
	FriendsSince       time.Time
}

// Repository provides persistence for friend requests, friendships, and blocks.
// When cache is non-nil, friendship-related reads are cached and writes invalidate
// the relevant keys in both directions.
type Repository struct {
	pool  *pgxpool.Pool
	cache *cache.Cache
}

// NewRepository returns a Repository backed by pool with caching disabled.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// NewRepositoryWithCache returns a Repository backed by pool that also caches and
// invalidates friendship data via the given cache.
func NewRepositoryWithCache(pool *pgxpool.Pool, c *cache.Cache) *Repository {
	return &Repository{pool: pool, cache: c}
}

const (
	// friendListCacheTTL is how long a cached friend list stays valid.
	friendListCacheTTL = 2 * time.Minute
	// friendshipCacheTTL is how long a cached friendship record stays valid.
	friendshipCacheTTL = 1 * time.Minute
	// pendingRequestsTTL is how long cached pending-request data stays valid.
	pendingRequestsTTL = 30 * time.Second
	// areFriendsCacheTTL is how long a cached are-friends boolean stays valid.
	areFriendsCacheTTL = 5 * time.Minute
)

// friendListCacheKey returns the cache key for a user's friend list.
func (r *Repository) friendListCacheKey(userID uuid.UUID) string {
	return fmt.Sprintf("friends:%s", userID.String())
}

// friendshipCacheKey returns the order-independent cache key for a friendship,
// sorting the two IDs so both directions map to the same key.
func (r *Repository) friendshipCacheKey(userID, friendID uuid.UUID) string {
	if userID.String() > friendID.String() {
		userID, friendID = friendID, userID
	}
	return fmt.Sprintf("friendship:%s:%s", userID.String(), friendID.String())
}

// pendingRequestsCacheKey returns the cache key for a user's pending requests.
func (r *Repository) pendingRequestsCacheKey(userID uuid.UUID) string {
	return fmt.Sprintf("friend_requests:%s", userID.String())
}

// areFriendsCacheKey returns the order-independent cache key for the are-friends
// flag, sorting the two IDs so both directions map to the same key.
func (r *Repository) areFriendsCacheKey(userID, friendID uuid.UUID) string {
	if userID.String() > friendID.String() {
		userID, friendID = friendID, userID
	}
	return fmt.Sprintf("are_friends:%s:%s", userID.String(), friendID.String())
}

// CreateFriendRequest inserts a pending request from fromUserID to toUserID,
// resetting an existing request between the same ordered pair back to pending on
// conflict, and invalidates the friendship caches for both users.
func (r *Repository) CreateFriendRequest(ctx context.Context, fromUserID, toUserID uuid.UUID) (*FriendRequest, error) {
	req := &FriendRequest{
		ID:         uuid.New(),
		FromUserID: fromUserID,
		ToUserID:   toUserID,
		Status:     "pending",
	}

	query := `
		INSERT INTO friend_requests (id, from_user_id, to_user_id, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (from_user_id, to_user_id)
		DO UPDATE SET status = 'pending', updated_at = NOW()
		RETURNING id, created_at, updated_at
	`

	err := r.pool.QueryRow(ctx, query,
		req.ID,
		req.FromUserID,
		req.ToUserID,
		req.Status,
	).Scan(&req.ID, &req.CreatedAt, &req.UpdatedAt)

	if err != nil {
		return nil, err
	}

	r.invalidateFriendshipCache(ctx, fromUserID, toUserID)

	return req, nil
}

// GetFriendRequest returns the request by ID, or NotFound if absent.
func (r *Repository) GetFriendRequest(ctx context.Context, id uuid.UUID) (*FriendRequest, error) {
	query := `
		SELECT id, from_user_id, to_user_id, status, created_at, updated_at
		FROM friend_requests
		WHERE id = $1
	`

	req := &FriendRequest{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&req.ID,
		&req.FromUserID,
		&req.ToUserID,
		&req.Status,
		&req.CreatedAt,
		&req.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("friend request not found")
	}

	return req, err
}

// GetFriendRequestBetweenUsers returns the most recent request in either direction
// between the two users, or nil, nil if none exists.
func (r *Repository) GetFriendRequestBetweenUsers(ctx context.Context, userID1, userID2 uuid.UUID) (*FriendRequest, error) {
	query := `
		SELECT id, from_user_id, to_user_id, status, created_at, updated_at
		FROM friend_requests
		WHERE (from_user_id = $1 AND to_user_id = $2) OR (from_user_id = $2 AND to_user_id = $1)
		ORDER BY created_at DESC
		LIMIT 1
	`

	req := &FriendRequest{}
	err := r.pool.QueryRow(ctx, query, userID1, userID2).Scan(
		&req.ID,
		&req.FromUserID,
		&req.ToUserID,
		&req.Status,
		&req.CreatedAt,
		&req.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}

	return req, err
}

// UpdateRequestStatus sets the request's status and bumps updated_at, returning
// NotFound if no request matches.
func (r *Repository) UpdateRequestStatus(ctx context.Context, id uuid.UUID, status string) error {
	query := `UPDATE friend_requests SET status = $2, updated_at = NOW() WHERE id = $1`

	result, err := r.pool.Exec(ctx, query, id, status)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("friend request not found")
	}

	return nil
}

// CreateFriendship inserts a friendship, storing the pair in sorted order so it is
// direction-independent (no-op if it already exists), and invalidates both users'
// friendship caches.
func (r *Repository) CreateFriendship(ctx context.Context, userID1, userID2 uuid.UUID) error {
	if userID1.String() > userID2.String() {
		userID1, userID2 = userID2, userID1
	}

	query := `
		INSERT INTO friendships (user_id1, user_id2)
		VALUES ($1, $2)
		ON CONFLICT (user_id1, user_id2) DO NOTHING
	`

	_, err := r.pool.Exec(ctx, query, userID1, userID2)
	if err != nil {
		return err
	}

	r.invalidateFriendshipCache(ctx, userID1, userID2)

	return nil
}

// DeleteFriendship removes the friendship (matching on the sorted ID pair) and
// invalidates both users' caches, returning NotFound if none existed.
func (r *Repository) DeleteFriendship(ctx context.Context, userID, friendID uuid.UUID) error {
	if userID.String() > friendID.String() {
		userID, friendID = friendID, userID
	}

	query := `DELETE FROM friendships WHERE user_id1 = $1 AND user_id2 = $2`

	result, err := r.pool.Exec(ctx, query, userID, friendID)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("friendship not found")
	}

	r.invalidateFriendshipCache(ctx, userID, friendID)

	return nil
}

// AreFriends reports whether the two users are friends, reading through the
// order-independent are-friends cache and caching the result on a miss.
func (r *Repository) AreFriends(ctx context.Context, userID, friendID uuid.UUID) (bool, error) {
	if r.cache != nil {
		var areFriends bool
		if err := r.cache.Get(ctx, r.areFriendsCacheKey(userID, friendID), &areFriends); err == nil {
			return areFriends, nil
		}
	}

	if userID.String() > friendID.String() {
		userID, friendID = friendID, userID
	}

	query := `SELECT EXISTS(SELECT 1 FROM friendships WHERE user_id1 = $1 AND user_id2 = $2)`

	var areFriends bool
	err := r.pool.QueryRow(ctx, query, userID, friendID).Scan(&areFriends)
	if err != nil {
		return false, err
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.areFriendsCacheKey(userID, friendID), areFriends, areFriendsCacheTTL)
	}

	return areFriends, nil
}

// IsBlocked reports whether blockerID has blocked blockedID. The relation is
// directional, so the argument order matters.
func (r *Repository) IsBlocked(ctx context.Context, blockerID, blockedID uuid.UUID) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM blocked_users WHERE blocker_id = $1 AND blocked_id = $2)`

	var isBlocked bool
	err := r.pool.QueryRow(ctx, query, blockerID, blockedID).Scan(&isBlocked)
	return isBlocked, err
}

// BlockUser records a directional block from blockerID to blockedID, ignoring an
// existing duplicate.
func (r *Repository) BlockUser(ctx context.Context, blockerID, blockedID uuid.UUID) error {
	query := `
		INSERT INTO blocked_users (blocker_id, blocked_id)
		VALUES ($1, $2)
		ON CONFLICT (blocker_id, blocked_id) DO NOTHING
	`

	_, err := r.pool.Exec(ctx, query, blockerID, blockedID)
	return err
}

// UnblockUser removes the directional block from blockerID to blockedID, returning
// NotFound if no such block existed.
func (r *Repository) UnblockUser(ctx context.Context, blockerID, blockedID uuid.UUID) error {
	query := `DELETE FROM blocked_users WHERE blocker_id = $1 AND blocked_id = $2`

	result, err := r.pool.Exec(ctx, query, blockerID, blockedID)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("block not found")
	}

	return nil
}

// ListBlockedUsers returns the IDs of users blocked by userID.
func (r *Repository) ListBlockedUsers(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	query := `SELECT blocked_id FROM blocked_users WHERE blocker_id = $1`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocked []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		blocked = append(blocked, id)
	}

	return blocked, rows.Err()
}

// ListFriends returns userID's friends (the other side of each friendship) ordered
// by display name, reading through the cache and caching non-empty results.
func (r *Repository) ListFriends(ctx context.Context, userID uuid.UUID) ([]*Friend, error) {
	if r.cache != nil {
		var cached []*Friend
		if err := r.cache.Get(ctx, r.friendListCacheKey(userID), &cached); err == nil {
			return cached, nil
		}
	}

	query := `
		SELECT u.id, u.handle, u.display_name, COALESCE(u.avatar_url, ''),
		       COALESCE(u.avatar_thumbnail_url, ''), COALESCE(u.status, 'offline'), f.created_at
		FROM friendships f
		JOIN users u ON (
			CASE
				WHEN f.user_id1 = $1 THEN f.user_id2 = u.id
				ELSE f.user_id1 = u.id
			END
		)
		WHERE f.user_id1 = $1 OR f.user_id2 = $1
		ORDER BY u.display_name
	`
	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var friends []*Friend
	for rows.Next() {
		f := &Friend{}
		if err := rows.Scan(
			&f.UserID, &f.Handle, &f.DisplayName,
			&f.AvatarURL, &f.AvatarThumbnailURL,
			&f.Status, &f.FriendsSince,
		); err != nil {
			return nil, err
		}
		friends = append(friends, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if r.cache != nil && len(friends) > 0 {
		_ = r.cache.Set(ctx, r.friendListCacheKey(userID), friends, friendListCacheTTL)
	}

	return friends, nil
}

// ListIncomingRequestsWithUsers returns pending requests addressed to userID,
// newest-first, joined with both users' profile fields.
func (r *Repository) ListIncomingRequestsWithUsers(ctx context.Context, userID uuid.UUID) ([]*FriendRequestWithUser, error) {
	query := `
		SELECT 
			fr.id, fr.from_user_id, fr.to_user_id, fr.status, fr.created_at, fr.updated_at,
			from_u.handle, from_u.display_name, COALESCE(from_u.avatar_url, ''),
			to_u.handle, to_u.display_name, COALESCE(to_u.avatar_url, '')
		FROM friend_requests fr
		JOIN users from_u ON fr.from_user_id = from_u.id
		JOIN users to_u ON fr.to_user_id = to_u.id
		WHERE fr.to_user_id = $1 AND fr.status = 'pending'
		ORDER BY fr.created_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []*FriendRequestWithUser
	for rows.Next() {
		req := &FriendRequestWithUser{}
		if err := rows.Scan(
			&req.ID, &req.FromUserID, &req.ToUserID, &req.Status, &req.CreatedAt, &req.UpdatedAt,
			&req.FromHandle, &req.FromDisplay, &req.FromAvatar,
			&req.ToHandle, &req.ToDisplay, &req.ToAvatar,
		); err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}

	return requests, rows.Err()
}

// ListOutgoingRequestsWithUsers returns pending requests sent by userID,
// newest-first, joined with both users' profile fields.
func (r *Repository) ListOutgoingRequestsWithUsers(ctx context.Context, userID uuid.UUID) ([]*FriendRequestWithUser, error) {
	query := `
		SELECT 
			fr.id, fr.from_user_id, fr.to_user_id, fr.status, fr.created_at, fr.updated_at,
			from_u.handle, from_u.display_name, COALESCE(from_u.avatar_url, ''),
			to_u.handle, to_u.display_name, COALESCE(to_u.avatar_url, '')
		FROM friend_requests fr
		JOIN users from_u ON fr.from_user_id = from_u.id
		JOIN users to_u ON fr.to_user_id = to_u.id
		WHERE fr.from_user_id = $1 AND fr.status = 'pending'
		ORDER BY fr.created_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []*FriendRequestWithUser
	for rows.Next() {
		req := &FriendRequestWithUser{}
		if err := rows.Scan(
			&req.ID, &req.FromUserID, &req.ToUserID, &req.Status, &req.CreatedAt, &req.UpdatedAt,
			&req.FromHandle, &req.FromDisplay, &req.FromAvatar,
			&req.ToHandle, &req.ToDisplay, &req.ToAvatar,
		); err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}

	return requests, rows.Err()
}

// invalidateFriendshipCache evicts every cache entry affected by a friendship
// change for both users: their friend lists, the shared friendship/are-friends
// keys, and both pending-request lists. A no-op when caching is disabled.
func (r *Repository) invalidateFriendshipCache(ctx context.Context, userID, friendID uuid.UUID) {
	if r.cache == nil {
		return
	}

	_ = r.cache.Delete(ctx,
		r.friendListCacheKey(userID),
		r.friendListCacheKey(friendID),
		r.friendshipCacheKey(userID, friendID),
		r.areFriendsCacheKey(userID, friendID),
		r.pendingRequestsCacheKey(userID),
		r.pendingRequestsCacheKey(friendID),
	)
}
