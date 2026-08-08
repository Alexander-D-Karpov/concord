package rooms

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

// Room is a room (server/guild) record. VoiceServerID is nil until a voice
// server is attached; DeletedAt is nil unless the room has been soft-deleted
// (queries filter on deleted_at IS NULL).
type Room struct {
	ID            uuid.UUID
	Name          string
	Description   string
	IsPrivate     bool
	CreatedBy     uuid.UUID
	VoiceServerID *uuid.UUID
	Region        string
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

// Member is a user's membership in a room. Role is one of "admin"/"moderator"/
// "member"; Nickname is nil when unset; Status and LastReadMessageID are joined
// in from the users and room_read_status tables.
type Member struct {
	RoomID            uuid.UUID
	UserID            uuid.UUID
	Role              string
	Nickname          *string
	Status            string
	LastReadMessageID int64
	JoinedAt          time.Time
}

// RoomInvite is a pending/accepted/rejected invitation for a user to join a
// room. Status is one of "pending"/"accepted"/"rejected".
type RoomInvite struct {
	ID            uuid.UUID
	RoomID        uuid.UUID
	InvitedUserID uuid.UUID
	InvitedBy     uuid.UUID
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// RoomInviteWithUsers is a RoomInvite denormalized with the room name and both
// the invited user's and the inviter's profile fields, for display without extra
// lookups.
type RoomInviteWithUsers struct {
	ID                     uuid.UUID
	RoomID                 uuid.UUID
	RoomName               string
	InvitedUserID          uuid.UUID
	InvitedBy              uuid.UUID
	Status                 string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	InvitedUserHandle      string
	InvitedUserDisplayName string
	InvitedUserAvatarURL   string
	InviterHandle          string
	InviterDisplayName     string
	InviterAvatarURL       string
}

// Repository owns the room, membership, and room-invite tables. cache is nil
// unless the repository was built with NewRepositoryWithCache; every method
// guards on cache != nil, so an uncached repository is fully functional.
type Repository struct {
	pool  *pgxpool.Pool
	cache *cache.Cache
}

// NewRepository builds a rooms Repository with caching disabled.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// NewRepositoryWithCache builds a rooms Repository that caches rooms, members,
// member lists, and per-user room lists; mutations invalidate the affected keys.
func NewRepositoryWithCache(pool *pgxpool.Pool, c *cache.Cache) *Repository {
	return &Repository{pool: pool, cache: c}
}

const (
	// roomCacheTTL is how long a room record is cached.
	roomCacheTTL = 5 * time.Minute
)

// roomCacheKey is the cache key for a single room record.
func (r *Repository) roomCacheKey(id uuid.UUID) string {
	return fmt.Sprintf("room:%s", id.String())
}

// memberCacheKey is the cache key for a single (room, user) membership.
func (r *Repository) memberCacheKey(roomID, userID uuid.UUID) string {
	return fmt.Sprintf("member:%s:%s", roomID.String(), userID.String())
}

// memberListCacheKey is the cache key for a room's full member list.
func (r *Repository) memberListCacheKey(roomID uuid.UUID) string {
	return fmt.Sprintf("members:%s", roomID.String())
}

// userRoomsCacheKey is the cache key for the list of rooms a user belongs to.
func (r *Repository) userRoomsCacheKey(userID uuid.UUID) string {
	return fmt.Sprintf("user:rooms:%s", userID.String())
}

// ListForUser is an alias for ListByUser, returning the rooms a user belongs to.
func (r *Repository) ListForUser(ctx context.Context, userID uuid.UUID) ([]*Room, error) {
	return r.ListByUser(ctx, userID)
}

// Create inserts a room, assigning a UUID when none is set and filling CreatedAt
// from the DB. On success it also warms the room cache with the new record.
func (r *Repository) Create(ctx context.Context, room *Room) error {
	query := `
		INSERT INTO rooms (id, name, description, is_private, created_by, voice_server_id, region)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at
	`

	if room.ID == uuid.Nil {
		room.ID = uuid.New()
	}

	err := r.pool.QueryRow(ctx, query,
		room.ID,
		room.Name,
		room.Description,
		room.IsPrivate,
		room.CreatedBy,
		room.VoiceServerID,
		room.Region,
	).Scan(&room.CreatedAt)

	if err != nil {
		return err
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.roomCacheKey(room.ID), room, roomCacheTTL)
	}

	return nil
}

// GetByID returns a non-deleted room, served from cache when present and
// back-filled on a miss. Returns NotFound for missing or soft-deleted rooms.
func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*Room, error) {
	if r.cache != nil {
		var cached Room
		if err := r.cache.Get(ctx, r.roomCacheKey(id), &cached); err == nil {
			return &cached, nil
		}
	}

	query := `
		SELECT id, name, COALESCE(description, ''), is_private, created_by, voice_server_id, COALESCE(region, ''), created_at, deleted_at
		FROM rooms
		WHERE id = $1 AND deleted_at IS NULL
	`

	room := &Room{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&room.ID,
		&room.Name,
		&room.Description,
		&room.IsPrivate,
		&room.CreatedBy,
		&room.VoiceServerID,
		&room.Region,
		&room.CreatedAt,
		&room.DeletedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("room not found")
	}
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		_ = r.cache.Set(ctx, r.roomCacheKey(id), room, roomCacheTTL)
	}

	return room, nil
}

// Update writes a room's mutable fields (skipping soft-deleted rows), returning
// NotFound when nothing matched, and evicts the room cache so the next read
// reloads.
func (r *Repository) Update(ctx context.Context, room *Room) error {
	query := `
		UPDATE rooms
		SET name = $2, description = $3, is_private = $4, voice_server_id = $5, region = $6
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query,
		room.ID,
		room.Name,
		room.Description,
		room.IsPrivate,
		room.VoiceServerID,
		room.Region,
	)

	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("room not found")
	}

	if r.cache != nil {
		// Also invalidate the settings cache, which mirrors is_private/slow_mode.
		_ = r.cache.Delete(ctx, r.roomCacheKey(room.ID), settingsCacheKey(room.ID))
	}

	return nil
}

// Delete soft-deletes a room (sets deleted_at), returning NotFound if it was
// already deleted or absent, and evicts the room cache.
func (r *Repository) Delete(ctx context.Context, roomID uuid.UUID) error {
	query := `UPDATE rooms SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	result, err := r.pool.Exec(ctx, query, roomID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.NotFound("room not found")
	}

	if r.cache != nil {
		_ = r.cache.Delete(ctx, r.roomCacheKey(roomID))
	}

	return nil
}

// ListByUser returns the non-deleted rooms a user is a member of, newest first.
// This query is uncached; the service layer caches the per-user list separately.
func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID) ([]*Room, error) {
	query := `
		SELECT r.id, r.name, r.description, r.is_private, r.created_by, 
		       r.voice_server_id, r.region, r.created_at
		FROM rooms r
		INNER JOIN memberships m ON r.id = m.room_id
		WHERE m.user_id = $1 AND r.deleted_at IS NULL
		ORDER BY r.created_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rooms []*Room
	for rows.Next() {
		room := &Room{}
		if err := rows.Scan(
			&room.ID,
			&room.Name,
			&room.Description,
			&room.IsPrivate,
			&room.CreatedBy,
			&room.VoiceServerID,
			&room.Region,
			&room.CreatedAt,
		); err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}

	return rooms, rows.Err()
}

// AddMember adds a user to a room with the given role, upserting the role if the
// membership already exists. It invalidates the member, member-list, and
// user-rooms caches together so all views of the change stay consistent.
func (r *Repository) AddMember(ctx context.Context, roomID, userID uuid.UUID, role string) error {
	query := `
		INSERT INTO memberships (room_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (room_id, user_id) DO UPDATE SET role = $3
	`

	_, err := r.pool.Exec(ctx, query, roomID, userID, role)
	if err != nil {
		return err
	}

	if r.cache != nil {
		_ = r.cache.Delete(ctx,
			r.memberCacheKey(roomID, userID),
			r.memberListCacheKey(roomID),
			r.userRoomsCacheKey(userID),
		)
	}

	return nil
}

// RemoveMember deletes a membership, returning NotFound if none matched, and
// invalidates the member, member-list, and user-rooms caches together.
func (r *Repository) RemoveMember(ctx context.Context, roomID, userID uuid.UUID) error {
	query := `DELETE FROM memberships WHERE room_id = $1 AND user_id = $2`

	result, err := r.pool.Exec(ctx, query, roomID, userID)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("member not found")
	}

	if r.cache != nil {
		_ = r.cache.Delete(ctx,
			r.memberCacheKey(roomID, userID),
			r.memberListCacheKey(roomID),
			r.userRoomsCacheKey(userID),
		)
	}

	return nil
}

// GetMember loads a single membership joined with the user's status and last-read
// position, returning NotFound when the user is not a member. This read is
// uncached (the boolean IsMember path is the cached one).
func (r *Repository) GetMember(ctx context.Context, roomID, userID uuid.UUID) (*Member, error) {
	query := `
		SELECT m.room_id, m.user_id, m.role, m.nickname, COALESCE(u.status, 'offline'), m.joined_at,
		       COALESCE(rs.last_read_message_id, 0)
		FROM memberships m
		JOIN users u ON m.user_id = u.id
		LEFT JOIN room_read_status rs ON m.room_id = rs.room_id AND m.user_id = rs.user_id
		WHERE m.room_id = $1 AND m.user_id = $2
	`

	member := &Member{}
	err := r.pool.QueryRow(ctx, query, roomID, userID).Scan(
		&member.RoomID,
		&member.UserID,
		&member.Role,
		&member.Nickname,
		&member.Status,
		&member.JoinedAt,
		&member.LastReadMessageID,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("member not found")
	}
	if err != nil {
		return nil, err
	}

	return member, nil
}

// IsMember reports whether a user belongs to a room. A cached member record is
// treated as a positive answer; otherwise it runs an EXISTS query. It does not
// populate the cache on a miss (only GetMember-driven paths do).
func (r *Repository) IsMember(ctx context.Context, roomID, userID uuid.UUID) (bool, error) {
	if r.cache != nil {
		var cached Member
		if err := r.cache.Get(ctx, r.memberCacheKey(roomID, userID), &cached); err == nil {
			return true, nil
		}
	}

	query := `SELECT EXISTS(SELECT 1 FROM memberships WHERE room_id = $1 AND user_id = $2)`

	var exists bool
	if err := r.pool.QueryRow(ctx, query, roomID, userID).Scan(&exists); err != nil {
		return false, err
	}

	return exists, nil
}

// ListMembers returns all members of a room joined with status and last-read
// position, ordered by join time. This read is uncached.
func (r *Repository) ListMembers(ctx context.Context, roomID uuid.UUID) ([]*Member, error) {
	query := `
		SELECT m.room_id, m.user_id, m.role, m.nickname, COALESCE(u.status, 'offline'), m.joined_at,
		       COALESCE(rs.last_read_message_id, 0)
		FROM memberships m
		JOIN users u ON m.user_id = u.id
		LEFT JOIN room_read_status rs ON m.room_id = rs.room_id AND m.user_id = rs.user_id
		WHERE m.room_id = $1
		ORDER BY m.joined_at ASC
	`

	rows, err := r.pool.Query(ctx, query, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*Member
	for rows.Next() {
		member := &Member{}
		if err := rows.Scan(
			&member.RoomID,
			&member.UserID,
			&member.Role,
			&member.Nickname,
			&member.Status,
			&member.JoinedAt,
			&member.LastReadMessageID,
		); err != nil {
			return nil, err
		}
		members = append(members, member)
	}

	return members, rows.Err()
}

// UpdateMemberRole changes a member's role, returning NotFound if absent, and
// invalidates the member and member-list caches (the user's room list is
// unaffected by a role change).
func (r *Repository) UpdateMemberRole(ctx context.Context, roomID, userID uuid.UUID, role string) error {
	query := `UPDATE memberships SET role = $3 WHERE room_id = $1 AND user_id = $2`

	result, err := r.pool.Exec(ctx, query, roomID, userID, role)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("member not found")
	}

	if r.cache != nil {
		_ = r.cache.Delete(ctx,
			r.memberCacheKey(roomID, userID),
			r.memberListCacheKey(roomID),
		)
	}

	return nil
}

// UpdateMemberNickname sets or clears a member's nickname (an empty string
// stores NULL), returning NotFound if the member is absent, and invalidates the
// member and member-list caches.
func (r *Repository) UpdateMemberNickname(ctx context.Context, roomID, userID uuid.UUID, nickname string) error {
	var nicknamePtr *string
	if nickname != "" {
		nicknamePtr = &nickname
	}

	query := `UPDATE memberships SET nickname = $3 WHERE room_id = $1 AND user_id = $2`

	result, err := r.pool.Exec(ctx, query, roomID, userID, nicknamePtr)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("member not found")
	}

	if r.cache != nil {
		_ = r.cache.Delete(ctx,
			r.memberCacheKey(roomID, userID),
			r.memberListCacheKey(roomID),
		)
	}

	return nil
}

// AssignVoiceServer sets a room's voice server and evicts the room cache on
// success so the change is visible on the next read.
func (r *Repository) AssignVoiceServer(ctx context.Context, roomID, serverID uuid.UUID) error {
	query := `UPDATE rooms SET voice_server_id = $2 WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, roomID, serverID)

	if err == nil && r.cache != nil {
		_ = r.cache.Delete(ctx, r.roomCacheKey(roomID))
	}

	return err
}

// CreateRoomInvite creates a pending invite, or re-opens an existing one for the
// same (room, invited user) by resetting it to pending and updating the inviter.
// The returned invite carries the DB-assigned id and timestamps.
func (r *Repository) CreateRoomInvite(
	ctx context.Context,
	roomID uuid.UUID,
	invitedUserID uuid.UUID,
	invitedBy uuid.UUID,
) (*RoomInvite, error) {
	inv := &RoomInvite{
		ID:            uuid.New(),
		RoomID:        roomID,
		InvitedUserID: invitedUserID,
		InvitedBy:     invitedBy,
		Status:        "pending",
	}

	query := `
        INSERT INTO room_invites (id, room_id, invited_user_id, invited_by, status)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (room_id, invited_user_id)
        DO UPDATE SET
            status = 'pending',
            invited_by = EXCLUDED.invited_by,
            updated_at = NOW()
        RETURNING id, created_at, updated_at
    `

	err := r.pool.QueryRow(
		ctx,
		query,
		inv.ID,
		inv.RoomID,
		inv.InvitedUserID,
		inv.InvitedBy,
		inv.Status,
	).Scan(&inv.ID, &inv.CreatedAt, &inv.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return inv, nil
}

// GetRoomInvite loads an invite by id, returning NotFound when absent.
func (r *Repository) GetRoomInvite(ctx context.Context, id uuid.UUID) (*RoomInvite, error) {
	query := `
		SELECT id, room_id, invited_user_id, invited_by, status, created_at, updated_at
		FROM room_invites
		WHERE id = $1
	`

	invite := &RoomInvite{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&invite.ID,
		&invite.RoomID,
		&invite.InvitedUserID,
		&invite.InvitedBy,
		&invite.Status,
		&invite.CreatedAt,
		&invite.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("invite not found")
	}

	return invite, err
}

// GetRoomInviteBetweenUsers returns the invite for a (room, invited user) pair,
// or (nil, nil) when none exists — the no-rows case is not an error, letting
// callers distinguish "no prior invite" from a lookup failure.
func (r *Repository) GetRoomInviteBetweenUsers(ctx context.Context, roomID, invitedUserID uuid.UUID) (*RoomInvite, error) {
	query := `
		SELECT id, room_id, invited_user_id, invited_by, status, created_at, updated_at
		FROM room_invites
		WHERE room_id = $1 AND invited_user_id = $2
	`

	invite := &RoomInvite{}
	err := r.pool.QueryRow(ctx, query, roomID, invitedUserID).Scan(
		&invite.ID,
		&invite.RoomID,
		&invite.InvitedUserID,
		&invite.InvitedBy,
		&invite.Status,
		&invite.CreatedAt,
		&invite.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}

	return invite, err
}

// GetRoomInviteWithUsers loads an invite by id joined with the room name and both
// users' profiles, returning NotFound when absent.
func (r *Repository) GetRoomInviteWithUsers(ctx context.Context, id uuid.UUID) (*RoomInviteWithUsers, error) {
	query := `
		SELECT 
			ri.id, ri.room_id, r.name as room_name,
			ri.invited_user_id, ri.invited_by, ri.status, ri.created_at, ri.updated_at,
			invited.handle, invited.display_name, COALESCE(invited.avatar_url, ''),
			inviter.handle, inviter.display_name, COALESCE(inviter.avatar_url, '')
		FROM room_invites ri
		JOIN rooms r ON ri.room_id = r.id
		JOIN users invited ON ri.invited_user_id = invited.id
		JOIN users inviter ON ri.invited_by = inviter.id
		WHERE ri.id = $1
	`

	invite := &RoomInviteWithUsers{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&invite.ID, &invite.RoomID, &invite.RoomName,
		&invite.InvitedUserID, &invite.InvitedBy, &invite.Status,
		&invite.CreatedAt, &invite.UpdatedAt,
		&invite.InvitedUserHandle, &invite.InvitedUserDisplayName, &invite.InvitedUserAvatarURL,
		&invite.InviterHandle, &invite.InviterDisplayName, &invite.InviterAvatarURL,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("invite not found")
	}

	return invite, err
}

// UpdateRoomInviteStatus transitions an invite to a new status (and bumps
// updated_at), returning NotFound when the invite is absent.
func (r *Repository) UpdateRoomInviteStatus(ctx context.Context, id uuid.UUID, status string) error {
	query := `UPDATE room_invites SET status = $2, updated_at = NOW() WHERE id = $1`

	result, err := r.pool.Exec(ctx, query, id, status)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("invite not found")
	}

	return nil
}

// ListIncomingRoomInvitesWithUsers returns the pending invites addressed to a
// user (where they are the invited party), newest first, with joined profiles.
func (r *Repository) ListIncomingRoomInvitesWithUsers(ctx context.Context, userID uuid.UUID) ([]*RoomInviteWithUsers, error) {
	query := `
		SELECT 
			ri.id, ri.room_id, r.name as room_name,
			ri.invited_user_id, ri.invited_by, ri.status, ri.created_at, ri.updated_at,
			invited.handle, invited.display_name, COALESCE(invited.avatar_url, ''),
			inviter.handle, inviter.display_name, COALESCE(inviter.avatar_url, '')
		FROM room_invites ri
		JOIN rooms r ON ri.room_id = r.id
		JOIN users invited ON ri.invited_user_id = invited.id
		JOIN users inviter ON ri.invited_by = inviter.id
		WHERE ri.invited_user_id = $1 AND ri.status = 'pending'
		ORDER BY ri.created_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []*RoomInviteWithUsers
	for rows.Next() {
		invite := &RoomInviteWithUsers{}
		if err := rows.Scan(
			&invite.ID, &invite.RoomID, &invite.RoomName,
			&invite.InvitedUserID, &invite.InvitedBy, &invite.Status,
			&invite.CreatedAt, &invite.UpdatedAt,
			&invite.InvitedUserHandle, &invite.InvitedUserDisplayName, &invite.InvitedUserAvatarURL,
			&invite.InviterHandle, &invite.InviterDisplayName, &invite.InviterAvatarURL,
		); err != nil {
			return nil, err
		}
		invites = append(invites, invite)
	}

	return invites, rows.Err()
}

// ListOutgoingRoomInvitesWithUsers returns the pending invites a user has sent
// (where they are the inviter), newest first, with joined profiles.
func (r *Repository) ListOutgoingRoomInvitesWithUsers(ctx context.Context, userID uuid.UUID) ([]*RoomInviteWithUsers, error) {
	query := `
		SELECT 
			ri.id, ri.room_id, r.name as room_name,
			ri.invited_user_id, ri.invited_by, ri.status, ri.created_at, ri.updated_at,
			invited.handle, invited.display_name, COALESCE(invited.avatar_url, ''),
			inviter.handle, inviter.display_name, COALESCE(inviter.avatar_url, '')
		FROM room_invites ri
		JOIN rooms r ON ri.room_id = r.id
		JOIN users invited ON ri.invited_user_id = invited.id
		JOIN users inviter ON ri.invited_by = inviter.id
		WHERE ri.invited_by = $1 AND ri.status = 'pending'
		ORDER BY ri.created_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []*RoomInviteWithUsers
	for rows.Next() {
		invite := &RoomInviteWithUsers{}
		if err := rows.Scan(
			&invite.ID, &invite.RoomID, &invite.RoomName,
			&invite.InvitedUserID, &invite.InvitedBy, &invite.Status,
			&invite.CreatedAt, &invite.UpdatedAt,
			&invite.InvitedUserHandle, &invite.InvitedUserDisplayName, &invite.InvitedUserAvatarURL,
			&invite.InviterHandle, &invite.InviterDisplayName, &invite.InviterAvatarURL,
		); err != nil {
			return nil, err
		}
		invites = append(invites, invite)
	}

	return invites, rows.Err()
}
