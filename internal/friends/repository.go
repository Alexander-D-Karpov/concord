package friends

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FriendRequest struct {
	ID         uuid.UUID
	FromUserID uuid.UUID
	ToUserID   uuid.UUID
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Friendship struct {
	UserID1   uuid.UUID
	UserID2   uuid.UUID
	CreatedAt time.Time
}

type Friend struct {
	UserID       uuid.UUID
	Handle       string
	DisplayName  string
	AvatarURL    string
	Status       string
	FriendsSince time.Time
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreateFriendRequest(ctx context.Context, fromUserID, toUserID uuid.UUID) (*FriendRequest, error) {
	query := `
		INSERT INTO friend_requests (from_user_id, to_user_id, status)
		VALUES ($1, $2, 'pending')
		RETURNING id, from_user_id, to_user_id, status, created_at, updated_at
	`

	req := &FriendRequest{}
	err := r.pool.QueryRow(ctx, query, fromUserID, toUserID).Scan(
		&req.ID,
		&req.FromUserID,
		&req.ToUserID,
		&req.Status,
		&req.CreatedAt,
		&req.UpdatedAt,
	)

	return req, err
}

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

func (r *Repository) UpdateRequestStatus(ctx context.Context, id uuid.UUID, status string) error {
	query := `
		UPDATE friend_requests
		SET status = $2, updated_at = NOW()
		WHERE id = $1
	`

	result, err := r.pool.Exec(ctx, query, id, status)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("friend request not found")
	}

	return nil
}

func (r *Repository) DeleteFriendRequest(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM friend_requests WHERE id = $1`
	result, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("friend request not found")
	}

	return nil
}

func (r *Repository) CreateFriendship(ctx context.Context, user1, user2 uuid.UUID) error {
	if user1.String() > user2.String() {
		user1, user2 = user2, user1
	}

	query := `
		INSERT INTO friendships (user_id1, user_id2)
		VALUES ($1, $2)
	`

	_, err := r.pool.Exec(ctx, query, user1, user2)
	return err
}

func (r *Repository) DeleteFriendship(ctx context.Context, user1, user2 uuid.UUID) error {
	if user1.String() > user2.String() {
		user1, user2 = user2, user1
	}

	query := `DELETE FROM friendships WHERE user_id1 = $1 AND user_id2 = $2`
	result, err := r.pool.Exec(ctx, query, user1, user2)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("friendship not found")
	}

	return nil
}

func (r *Repository) AreFriends(ctx context.Context, user1, user2 uuid.UUID) (bool, error) {
	if user1.String() > user2.String() {
		user1, user2 = user2, user1
	}

	query := `
		SELECT EXISTS(
			SELECT 1 FROM friendships 
			WHERE user_id1 = $1 AND user_id2 = $2
		)
	`

	var exists bool
	err := r.pool.QueryRow(ctx, query, user1, user2).Scan(&exists)
	return exists, err
}

func (r *Repository) ListFriends(ctx context.Context, userID uuid.UUID) ([]*Friend, error) {
	query := `
		SELECT u.id, u.handle, u.display_name, u.avatar_url, u.status, f.created_at
		FROM users u
		INNER JOIN friendships f ON (
			(f.user_id1 = $1 AND f.user_id2 = u.id) OR
			(f.user_id2 = $1 AND f.user_id1 = u.id)
		)
		WHERE u.deleted_at IS NULL
		ORDER BY u.display_name ASC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var friends []*Friend
	for rows.Next() {
		friend := &Friend{}
		if err := rows.Scan(
			&friend.UserID,
			&friend.Handle,
			&friend.DisplayName,
			&friend.AvatarURL,
			&friend.Status,
			&friend.FriendsSince,
		); err != nil {
			return nil, err
		}
		friends = append(friends, friend)
	}

	return friends, rows.Err()
}

func (r *Repository) ListIncomingRequests(ctx context.Context, userID uuid.UUID) ([]*FriendRequest, error) {
	query := `
		SELECT id, from_user_id, to_user_id, status, created_at, updated_at
		FROM friend_requests
		WHERE to_user_id = $1 AND status = 'pending'
		ORDER BY created_at DESC
	`

	return r.queryRequests(ctx, query, userID)
}

func (r *Repository) ListOutgoingRequests(ctx context.Context, userID uuid.UUID) ([]*FriendRequest, error) {
	query := `
		SELECT id, from_user_id, to_user_id, status, created_at, updated_at
		FROM friend_requests
		WHERE from_user_id = $1 AND status = 'pending'
		ORDER BY created_at DESC
	`

	return r.queryRequests(ctx, query, userID)
}

func (r *Repository) queryRequests(ctx context.Context, query string, userID uuid.UUID) ([]*FriendRequest, error) {
	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []*FriendRequest
	for rows.Next() {
		req := &FriendRequest{}
		if err := rows.Scan(
			&req.ID,
			&req.FromUserID,
			&req.ToUserID,
			&req.Status,
			&req.CreatedAt,
			&req.UpdatedAt,
		); err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}

	return requests, rows.Err()
}

func (r *Repository) BlockUser(ctx context.Context, blockerID, blockedID uuid.UUID) error {
	query := `
		INSERT INTO blocked_users (blocker_id, blocked_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`

	_, err := r.pool.Exec(ctx, query, blockerID, blockedID)
	return err
}

func (r *Repository) UnblockUser(ctx context.Context, blockerID, blockedID uuid.UUID) error {
	query := `DELETE FROM blocked_users WHERE blocker_id = $1 AND blocked_id = $2`
	_, err := r.pool.Exec(ctx, query, blockerID, blockedID)
	return err
}

func (r *Repository) IsBlocked(ctx context.Context, blockerID, blockedID uuid.UUID) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1 FROM blocked_users 
			WHERE blocker_id = $1 AND blocked_id = $2
		)
	`

	var blocked bool
	err := r.pool.QueryRow(ctx, query, blockerID, blockedID).Scan(&blocked)
	return blocked, err
}

func (r *Repository) ListBlockedUsers(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	query := `
		SELECT blocked_id FROM blocked_users
		WHERE blocker_id = $1
		ORDER BY created_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blockedIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		blockedIDs = append(blockedIDs, id)
	}

	return blockedIDs, rows.Err()
}

func (r *Repository) GetFriendRequestBetweenUsers(ctx context.Context, user1, user2 uuid.UUID) (*FriendRequest, error) {
	query := `
		SELECT id, from_user_id, to_user_id, status, created_at, updated_at
		FROM friend_requests
		WHERE (from_user_id = $1 AND to_user_id = $2) 
		   OR (from_user_id = $2 AND to_user_id = $1)
		ORDER BY created_at DESC
		LIMIT 1
	`

	req := &FriendRequest{}
	err := r.pool.QueryRow(ctx, query, user1, user2).Scan(
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

func (r *Repository) ListIncomingRequestsWithUsers(ctx context.Context, userID uuid.UUID) ([]*FriendRequestWithUser, error) {
	query := `
		SELECT 
			fr.id, fr.from_user_id, fr.to_user_id, fr.status, fr.created_at, fr.updated_at,
			u_from.handle as from_handle, u_from.display_name as from_display, u_from.avatar_url as from_avatar,
			u_to.handle as to_handle, u_to.display_name as to_display, u_to.avatar_url as to_avatar
		FROM friend_requests fr
		INNER JOIN users u_from ON fr.from_user_id = u_from.id
		INNER JOIN users u_to ON fr.to_user_id = u_to.id
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
			&req.ID,
			&req.FromUserID,
			&req.ToUserID,
			&req.Status,
			&req.CreatedAt,
			&req.UpdatedAt,
			&req.FromHandle,
			&req.FromDisplay,
			&req.FromAvatar,
			&req.ToHandle,
			&req.ToDisplay,
			&req.ToAvatar,
		); err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}

	return requests, rows.Err()
}

func (r *Repository) ListOutgoingRequestsWithUsers(ctx context.Context, userID uuid.UUID) ([]*FriendRequestWithUser, error) {
	query := `
		SELECT 
			fr.id, fr.from_user_id, fr.to_user_id, fr.status, fr.created_at, fr.updated_at,
			u_from.handle as from_handle, u_from.display_name as from_display, u_from.avatar_url as from_avatar,
			u_to.handle as to_handle, u_to.display_name as to_display, u_to.avatar_url as to_avatar
		FROM friend_requests fr
		INNER JOIN users u_from ON fr.from_user_id = u_from.id
		INNER JOIN users u_to ON fr.to_user_id = u_to.id
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
			&req.ID,
			&req.FromUserID,
			&req.ToUserID,
			&req.Status,
			&req.CreatedAt,
			&req.UpdatedAt,
			&req.FromHandle,
			&req.FromDisplay,
			&req.FromAvatar,
			&req.ToHandle,
			&req.ToDisplay,
			&req.ToAvatar,
		); err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}

	return requests, rows.Err()
}
