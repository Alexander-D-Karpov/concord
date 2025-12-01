package rooms

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Room struct {
	ID            uuid.UUID
	Name          string
	CreatedBy     uuid.UUID
	VoiceServerID *uuid.UUID
	Region        *string
	Description   *string
	IsPrivate     bool
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

type Member struct {
	RoomID   uuid.UUID
	UserID   uuid.UUID
	Role     string
	JoinedAt time.Time
	Nickname *string
	Status   string
}

type Repository struct {
	pool *pgxpool.Pool
}

type RoomInvite struct {
	ID            uuid.UUID
	RoomID        uuid.UUID
	InvitedUserID uuid.UUID
	InvitedBy     uuid.UUID
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

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

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, room *Room) error {
	query := `
		INSERT INTO rooms (id, name, created_by, voice_server_id, region, description, is_private)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at
	`

	if room.ID == uuid.Nil {
		room.ID = uuid.New()
	}

	err := r.pool.QueryRow(ctx, query,
		room.ID,
		room.Name,
		room.CreatedBy,
		room.VoiceServerID,
		room.Region,
		room.Description,
		room.IsPrivate,
	).Scan(&room.CreatedAt)

	return err
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*Room, error) {
	query := `
		SELECT id, name, created_by, voice_server_id, region, description, is_private, created_at, deleted_at
		FROM rooms
		WHERE id = $1 AND deleted_at IS NULL
	`

	room := &Room{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&room.ID,
		&room.Name,
		&room.CreatedBy,
		&room.VoiceServerID,
		&room.Region,
		&room.Description,
		&room.IsPrivate,
		&room.CreatedAt,
		&room.DeletedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("room not found")
	}
	if err != nil {
		return nil, err
	}

	return room, nil
}

func (r *Repository) Update(ctx context.Context, room *Room) error {
	query := `
		UPDATE rooms
		SET name = $2, description = $3, is_private = $4
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query, room.ID, room.Name, room.Description, room.IsPrivate)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("room not found")
	}

	return nil
}

func (r *Repository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	query := `
		UPDATE rooms
		SET deleted_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("room not found")
	}

	return nil
}

func (r *Repository) ListForUser(ctx context.Context, userID uuid.UUID) ([]*Room, error) {
	query := `
		SELECT r.id, r.name, r.created_by, r.voice_server_id, r.region, r.description, r.is_private, r.created_at, r.deleted_at
		FROM rooms r
		JOIN memberships m ON r.id = m.room_id
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
			&room.CreatedBy,
			&room.VoiceServerID,
			&room.Region,
			&room.Description,
			&room.IsPrivate,
			&room.CreatedAt,
			&room.DeletedAt,
		); err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}

	return rooms, rows.Err()
}

func (r *Repository) UpdateVoiceServer(ctx context.Context, roomID, serverID uuid.UUID) error {
	query := `
		UPDATE rooms
		SET voice_server_id = $2
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.pool.Exec(ctx, query, roomID, serverID)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("room not found")
	}

	return nil
}

func (r *Repository) AddMember(ctx context.Context, roomID, userID uuid.UUID, role string) error {
	query := `
		INSERT INTO memberships (room_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (room_id, user_id) DO NOTHING
	`

	_, err := r.pool.Exec(ctx, query, roomID, userID, role)
	return err
}

func (r *Repository) RemoveMember(ctx context.Context, roomID, userID uuid.UUID) error {
	query := `DELETE FROM memberships WHERE room_id = $1 AND user_id = $2`
	_, err := r.pool.Exec(ctx, query, roomID, userID)
	return err
}

func (r *Repository) GetMember(ctx context.Context, roomID, userID uuid.UUID) (*Member, error) {
	query := `
		SELECT 
			m.room_id,
			m.user_id,
			m.role,
			m.joined_at,
			m.nickname,
			u.status
		FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.room_id = $1 AND m.user_id = $2
	`

	member := &Member{}
	err := r.pool.QueryRow(ctx, query, roomID, userID).Scan(
		&member.RoomID,
		&member.UserID,
		&member.Role,
		&member.JoinedAt,
		&member.Nickname,
		&member.Status,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("member not found")
	}
	if err != nil {
		return nil, err
	}

	return member, nil
}

func (r *Repository) ListMembers(ctx context.Context, roomID uuid.UUID) ([]*Member, error) {
	query := `
        SELECT
            m.user_id,
            m.room_id,
            m.role,
            m.joined_at,
            m.nickname,
            u.status
        FROM memberships m
        JOIN users u ON u.id = m.user_id
        WHERE m.room_id = $1
        ORDER BY u.display_name ASC
    `

	rows, err := r.pool.Query(ctx, query, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*Member
	for rows.Next() {
		m := &Member{}
		if err := rows.Scan(
			&m.UserID,
			&m.RoomID,
			&m.Role,
			&m.JoinedAt,
			&m.Nickname,
			&m.Status,
		); err != nil {
			return nil, err
		}
		members = append(members, m)
	}

	return members, rows.Err()
}

func (r *Repository) UpdateMemberRole(ctx context.Context, roomID, userID uuid.UUID, role string) error {
	query := `
		UPDATE memberships
		SET role = $3
		WHERE room_id = $1 AND user_id = $2
	`

	result, err := r.pool.Exec(ctx, query, roomID, userID, role)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("member not found")
	}

	return nil
}

func (r *Repository) UpdateMemberNickname(ctx context.Context, roomID, userID uuid.UUID, nickname string) error {
	query := `
		UPDATE memberships
		SET nickname = $3
		WHERE room_id = $1 AND user_id = $2
	`

	result, err := r.pool.Exec(ctx, query, roomID, userID, nickname)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("member not found")
	}

	return nil
}

func (r *Repository) Pool() *pgxpool.Pool {
	return r.pool
}

func (r *Repository) CreateRoomInvite(ctx context.Context, roomID, invitedUserID, invitedBy uuid.UUID) (*RoomInvite, error) {
	query := `
		INSERT INTO room_invites (room_id, invited_user_id, invited_by, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id, room_id, invited_user_id, invited_by, status, created_at, updated_at
	`

	inv := &RoomInvite{}
	err := r.pool.QueryRow(ctx, query, roomID, invitedUserID, invitedBy).Scan(
		&inv.ID,
		&inv.RoomID,
		&inv.InvitedUserID,
		&inv.InvitedBy,
		&inv.Status,
		&inv.CreatedAt,
		&inv.UpdatedAt,
	)

	return inv, err
}

func (r *Repository) GetRoomInvite(ctx context.Context, id uuid.UUID) (*RoomInvite, error) {
	query := `
		SELECT id, room_id, invited_user_id, invited_by, status, created_at, updated_at
		FROM room_invites
		WHERE id = $1
	`

	inv := &RoomInvite{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&inv.ID,
		&inv.RoomID,
		&inv.InvitedUserID,
		&inv.InvitedBy,
		&inv.Status,
		&inv.CreatedAt,
		&inv.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("room invite not found")
	}

	return inv, err
}

func (r *Repository) GetRoomInviteWithUsers(ctx context.Context, id uuid.UUID) (*RoomInviteWithUsers, error) {
	query := `
		SELECT 
			ri.id, ri.room_id, r.name as room_name, ri.invited_user_id, ri.invited_by, 
			ri.status, ri.created_at, ri.updated_at,
			u_invited.handle, u_invited.display_name, u_invited.avatar_url,
			u_inviter.handle, u_inviter.display_name, u_inviter.avatar_url
		FROM room_invites ri
		INNER JOIN rooms r ON ri.room_id = r.id
		INNER JOIN users u_invited ON ri.invited_user_id = u_invited.id
		INNER JOIN users u_inviter ON ri.invited_by = u_inviter.id
		WHERE ri.id = $1
	`

	inv := &RoomInviteWithUsers{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&inv.ID,
		&inv.RoomID,
		&inv.RoomName,
		&inv.InvitedUserID,
		&inv.InvitedBy,
		&inv.Status,
		&inv.CreatedAt,
		&inv.UpdatedAt,
		&inv.InvitedUserHandle,
		&inv.InvitedUserDisplayName,
		&inv.InvitedUserAvatarURL,
		&inv.InviterHandle,
		&inv.InviterDisplayName,
		&inv.InviterAvatarURL,
	)

	if err == pgx.ErrNoRows {
		return nil, errors.NotFound("room invite not found")
	}

	return inv, err
}

func (r *Repository) UpdateRoomInviteStatus(ctx context.Context, id uuid.UUID, status string) error {
	query := `
		UPDATE room_invites
		SET status = $2, updated_at = NOW()
		WHERE id = $1
	`

	result, err := r.pool.Exec(ctx, query, id, status)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return errors.NotFound("room invite not found")
	}

	return nil
}

func (r *Repository) GetRoomInviteBetweenUsers(ctx context.Context, roomID, invitedUserID uuid.UUID) (*RoomInvite, error) {
	query := `
		SELECT id, room_id, invited_user_id, invited_by, status, created_at, updated_at
		FROM room_invites
		WHERE room_id = $1 AND invited_user_id = $2 AND status = 'pending'
		ORDER BY created_at DESC
		LIMIT 1
	`

	inv := &RoomInvite{}
	err := r.pool.QueryRow(ctx, query, roomID, invitedUserID).Scan(
		&inv.ID,
		&inv.RoomID,
		&inv.InvitedUserID,
		&inv.InvitedBy,
		&inv.Status,
		&inv.CreatedAt,
		&inv.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}

	return inv, err
}

func (r *Repository) ListIncomingRoomInvitesWithUsers(ctx context.Context, userID uuid.UUID) ([]*RoomInviteWithUsers, error) {
	query := `
		SELECT 
			ri.id, ri.room_id, r.name as room_name, ri.invited_user_id, ri.invited_by, 
			ri.status, ri.created_at, ri.updated_at,
			u_invited.handle, u_invited.display_name, u_invited.avatar_url,
			u_inviter.handle, u_inviter.display_name, u_inviter.avatar_url
		FROM room_invites ri
		INNER JOIN rooms r ON ri.room_id = r.id
		INNER JOIN users u_invited ON ri.invited_user_id = u_invited.id
		INNER JOIN users u_inviter ON ri.invited_by = u_inviter.id
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
		inv := &RoomInviteWithUsers{}
		if err := rows.Scan(
			&inv.ID,
			&inv.RoomID,
			&inv.RoomName,
			&inv.InvitedUserID,
			&inv.InvitedBy,
			&inv.Status,
			&inv.CreatedAt,
			&inv.UpdatedAt,
			&inv.InvitedUserHandle,
			&inv.InvitedUserDisplayName,
			&inv.InvitedUserAvatarURL,
			&inv.InviterHandle,
			&inv.InviterDisplayName,
			&inv.InviterAvatarURL,
		); err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}

	return invites, rows.Err()
}

func (r *Repository) ListOutgoingRoomInvitesWithUsers(ctx context.Context, userID uuid.UUID) ([]*RoomInviteWithUsers, error) {
	query := `
		SELECT 
			ri.id, ri.room_id, r.name as room_name, ri.invited_user_id, ri.invited_by, 
			ri.status, ri.created_at, ri.updated_at,
			u_invited.handle, u_invited.display_name, u_invited.avatar_url,
			u_inviter.handle, u_inviter.display_name, u_inviter.avatar_url
		FROM room_invites ri
		INNER JOIN rooms r ON ri.room_id = r.id
		INNER JOIN users u_invited ON ri.invited_user_id = u_invited.id
		INNER JOIN users u_inviter ON ri.invited_by = u_inviter.id
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
		inv := &RoomInviteWithUsers{}
		if err := rows.Scan(
			&inv.ID,
			&inv.RoomID,
			&inv.RoomName,
			&inv.InvitedUserID,
			&inv.InvitedBy,
			&inv.Status,
			&inv.CreatedAt,
			&inv.UpdatedAt,
			&inv.InvitedUserHandle,
			&inv.InvitedUserDisplayName,
			&inv.InvitedUserAvatarURL,
			&inv.InviterHandle,
			&inv.InviterDisplayName,
			&inv.InviterAvatarURL,
		); err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}

	return invites, rows.Err()
}

func (r *Repository) IsMember(ctx context.Context, roomID, userID uuid.UUID) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1 FROM memberships 
			WHERE room_id = $1 AND user_id = $2
		)
	`

	var exists bool
	err := r.pool.QueryRow(ctx, query, roomID, userID).Scan(&exists)
	return exists, err
}
