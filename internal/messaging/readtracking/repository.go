package readtracking

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository is the read-tracking data-access layer over pool for both room and
// DM read status and unread counts.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a Repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// RoomReadStatus is a user's last-read position in one room. LastReadMessageID is
// a Snowflake ID (0 when the user has never read the room).
type RoomReadStatus struct {
	UserID            uuid.UUID
	RoomID            uuid.UUID
	LastReadMessageID int64
	UpdatedAt         time.Time
}

// DMReadStatus is the DM-channel counterpart of RoomReadStatus.
type DMReadStatus struct {
	UserID            uuid.UUID
	ChannelID         uuid.UUID
	LastReadMessageID int64
	UpdatedAt         time.Time
}

// RoomUnreadInfo summarizes one room's unread state for a user: how many unread
// messages, the last-read position, and the latest message's ID/time.
type RoomUnreadInfo struct {
	RoomID            uuid.UUID
	UnreadCount       int32
	LastReadMessageID int64
	LatestMessageID   int64
	LatestMessageAt   time.Time
}

// DMUnreadInfo is the DM-channel counterpart of RoomUnreadInfo.
type DMUnreadInfo struct {
	ChannelID         uuid.UUID
	UnreadCount       int32
	LastReadMessageID int64
	LatestMessageID   int64
	LatestMessageAt   time.Time
}

// GetRoomReadStatus returns the user's read status for a room. When no row exists
// it returns a zero-value status (LastReadMessageID 0) rather than an error, so
// callers can treat "never read" uniformly.
func (r *Repository) GetRoomReadStatus(ctx context.Context, userID, roomID uuid.UUID) (*RoomReadStatus, error) {
	query := `
		SELECT user_id, room_id, last_read_message_id, updated_at
		FROM room_read_status
		WHERE user_id = $1 AND room_id = $2
	`

	status := &RoomReadStatus{}
	err := r.pool.QueryRow(ctx, query, userID, roomID).Scan(
		&status.UserID,
		&status.RoomID,
		&status.LastReadMessageID,
		&status.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return &RoomReadStatus{
			UserID:            userID,
			RoomID:            roomID,
			LastReadMessageID: 0,
		}, nil
	}

	return status, err
}

// MarkRoomAsRead upserts the user's last-read position for a room. It only ever
// advances the position (GREATEST of stored and new), so an out-of-order or
// older messageID cannot move it backward.
func (r *Repository) MarkRoomAsRead(ctx context.Context, userID, roomID uuid.UUID, messageID int64) error {
	query := `
		INSERT INTO room_read_status (user_id, room_id, last_read_message_id, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id, room_id) DO UPDATE
		SET last_read_message_id = GREATEST(room_read_status.last_read_message_id, EXCLUDED.last_read_message_id),
		    updated_at = NOW()
	`
	_, err := r.pool.Exec(ctx, query, userID, roomID, messageID)
	return err
}

// GetRoomUnreadCount counts non-deleted messages in a room with an ID greater
// than the user's last-read position (0 if never read), relying on Snowflake IDs
// being time-ordered.
func (r *Repository) GetRoomUnreadCount(ctx context.Context, userID, roomID uuid.UUID) (int32, error) {
	query := `
		SELECT COUNT(*)::int
		FROM messages m
		WHERE m.room_id = $2 
		  AND m.deleted_at IS NULL
		  AND m.id > COALESCE(
		      (SELECT last_read_message_id FROM room_read_status WHERE user_id = $1 AND room_id = $2),
		      0
		  )
	`

	var count int32
	err := r.pool.QueryRow(ctx, query, userID, roomID).Scan(&count)
	return count, err
}

// GetAllRoomUnreadCounts returns unread info for every room the user is a member
// of that has unread messages (rooms fully read are omitted), ordered by latest
// message time descending.
func (r *Repository) GetAllRoomUnreadCounts(ctx context.Context, userID uuid.UUID) ([]RoomUnreadInfo, error) {
	query := `
		WITH user_rooms AS (
			SELECT room_id FROM memberships WHERE user_id = $1
		),
		read_positions AS (
			SELECT room_id, last_read_message_id
			FROM room_read_status
			WHERE user_id = $1
		)
		SELECT 
			ur.room_id,
			COALESCE(rp.last_read_message_id, 0) as last_read_message_id,
			COALESCE(latest.id, 0) as latest_message_id,
			COALESCE(latest.created_at, NOW()) as latest_message_at,
			COUNT(m.id)::int as unread_count
		FROM user_rooms ur
		LEFT JOIN read_positions rp ON rp.room_id = ur.room_id
		LEFT JOIN LATERAL (
			SELECT id, created_at 
			FROM messages 
			WHERE room_id = ur.room_id AND deleted_at IS NULL 
			ORDER BY id DESC LIMIT 1
		) latest ON true
		LEFT JOIN messages m ON m.room_id = ur.room_id 
			AND m.deleted_at IS NULL 
			AND m.id > COALESCE(rp.last_read_message_id, 0)
		WHERE latest.id > COALESCE(rp.last_read_message_id, 0)
		GROUP BY ur.room_id, rp.last_read_message_id, latest.id, latest.created_at
		ORDER BY latest.created_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RoomUnreadInfo
	for rows.Next() {
		var info RoomUnreadInfo
		if err := rows.Scan(
			&info.RoomID,
			&info.LastReadMessageID,
			&info.LatestMessageID,
			&info.LatestMessageAt,
			&info.UnreadCount,
		); err != nil {
			return nil, err
		}
		results = append(results, info)
	}

	return results, rows.Err()
}

// GetDMReadStatus is the DM counterpart of GetRoomReadStatus, returning a
// zero-value status when the user has never read the channel.
func (r *Repository) GetDMReadStatus(ctx context.Context, userID, channelID uuid.UUID) (*DMReadStatus, error) {
	query := `
		SELECT user_id, channel_id, last_read_message_id, updated_at
		FROM dm_read_status
		WHERE user_id = $1 AND channel_id = $2
	`

	status := &DMReadStatus{}
	err := r.pool.QueryRow(ctx, query, userID, channelID).Scan(
		&status.UserID,
		&status.ChannelID,
		&status.LastReadMessageID,
		&status.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return &DMReadStatus{
			UserID:            userID,
			ChannelID:         channelID,
			LastReadMessageID: 0,
		}, nil
	}

	return status, err
}

// MarkDMAsRead is the DM counterpart of MarkRoomAsRead; it likewise only advances
// the last-read position via GREATEST.
func (r *Repository) MarkDMAsRead(ctx context.Context, userID, channelID uuid.UUID, messageID int64) error {
	query := `
		INSERT INTO dm_read_status (user_id, channel_id, last_read_message_id, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id, channel_id) DO UPDATE
		SET last_read_message_id = GREATEST(dm_read_status.last_read_message_id, EXCLUDED.last_read_message_id),
		    updated_at = NOW()
	`
	_, err := r.pool.Exec(ctx, query, userID, channelID, messageID)
	return err
}

// GetDMUnreadCount is the DM counterpart of GetRoomUnreadCount.
func (r *Repository) GetDMUnreadCount(ctx context.Context, userID, channelID uuid.UUID) (int32, error) {
	query := `
		SELECT COUNT(*)::int
		FROM dm_messages m
		WHERE m.channel_id = $2 
		  AND m.deleted_at IS NULL
		  AND m.id > COALESCE(
		      (SELECT last_read_message_id FROM dm_read_status WHERE user_id = $1 AND channel_id = $2),
		      0
		  )
	`

	var count int32
	err := r.pool.QueryRow(ctx, query, userID, channelID).Scan(&count)
	return count, err
}

// GetAllDMUnreadCounts is the DM counterpart of GetAllRoomUnreadCounts, covering
// the user's DM channels and omitting fully-read ones, ordered by latest message
// time descending.
func (r *Repository) GetAllDMUnreadCounts(ctx context.Context, userID uuid.UUID) ([]DMUnreadInfo, error) {
	query := `
		WITH user_channels AS (
			SELECT channel_id FROM dm_participants WHERE user_id = $1
		),
		channel_latest AS (
			SELECT 
				m.channel_id,
				MAX(m.id) as latest_message_id,
				MAX(m.created_at) as latest_message_at
			FROM dm_messages m
			INNER JOIN user_channels uc ON uc.channel_id = m.channel_id
			WHERE m.deleted_at IS NULL
			GROUP BY m.channel_id
		),
		read_status AS (
			SELECT channel_id, last_read_message_id
			FROM dm_read_status
			WHERE user_id = $1
		)
		SELECT 
			cl.channel_id,
			COALESCE(rs.last_read_message_id, 0) as last_read_message_id,
			cl.latest_message_id,
			cl.latest_message_at,
			(
				SELECT COUNT(*)::int 
				FROM dm_messages 
				WHERE channel_id = cl.channel_id 
				  AND deleted_at IS NULL 
				  AND id > COALESCE(rs.last_read_message_id, 0)
			) as unread_count
		FROM channel_latest cl
		LEFT JOIN read_status rs ON rs.channel_id = cl.channel_id
		WHERE cl.latest_message_id > COALESCE(rs.last_read_message_id, 0)
		ORDER BY cl.latest_message_at DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DMUnreadInfo
	for rows.Next() {
		var info DMUnreadInfo
		if err := rows.Scan(
			&info.ChannelID,
			&info.LastReadMessageID,
			&info.LatestMessageID,
			&info.LatestMessageAt,
			&info.UnreadCount,
		); err != nil {
			return nil, err
		}
		results = append(results, info)
	}

	return results, rows.Err()
}

// GetDMChannelParticipants returns the user IDs in a DM channel, used to fan out
// read receipts to the other participants.
func (r *Repository) GetDMChannelParticipants(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error) {
	query := `SELECT user_id FROM dm_participants WHERE channel_id = $1`

	rows, err := r.pool.Query(ctx, query, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var participants []uuid.UUID
	for rows.Next() {
		var userID uuid.UUID
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		participants = append(participants, userID)
	}

	return participants, rows.Err()
}
