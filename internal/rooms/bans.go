package rooms

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Ban is an active or historical room ban. ExpiresAt is nil for a permanent ban.
type Ban struct {
	RoomID    uuid.UUID
	UserID    uuid.UUID
	BannedBy  uuid.UUID
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// Mute is a room mute (voice), keyed by (room, user).
type Mute struct {
	RoomID    uuid.UUID
	UserID    uuid.UUID
	MutedBy   uuid.UUID
	CreatedAt time.Time
}

// AddBan records (or refreshes) a ban of userID in roomID by bannedBy. A nil
// expiresAt means a permanent ban; an existing ban for the pair is overwritten.
func (r *Repository) AddBan(ctx context.Context, roomID, userID, bannedBy uuid.UUID, expiresAt *time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO room_bans (room_id, user_id, banned_by, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (room_id, user_id) DO UPDATE SET
			banned_by = EXCLUDED.banned_by,
			expires_at = EXCLUDED.expires_at,
			created_at = NOW()
	`, roomID, userID, bannedBy, expiresAt)
	return err
}

// RemoveBan lifts any ban of userID in roomID, reporting whether a row was removed
// (false means there was no ban to lift).
func (r *Repository) RemoveBan(ctx context.Context, roomID, userID uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM room_bans WHERE room_id = $1 AND user_id = $2`, roomID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// IsBanned reports whether userID has an active (non-expired) ban in roomID.
func (r *Repository) IsBanned(ctx context.Context, roomID, userID uuid.UUID) (bool, error) {
	var banned bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM room_bans
			WHERE room_id = $1 AND user_id = $2
			  AND (expires_at IS NULL OR expires_at > NOW())
		)
	`, roomID, userID).Scan(&banned)
	return banned, err
}

// ListBans returns the active (non-expired) bans in roomID, newest first.
func (r *Repository) ListBans(ctx context.Context, roomID uuid.UUID) ([]Ban, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT room_id, user_id, banned_by, expires_at, created_at
		FROM room_bans
		WHERE room_id = $1 AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY created_at DESC
	`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bans []Ban
	for rows.Next() {
		var b Ban
		if err := rows.Scan(&b.RoomID, &b.UserID, &b.BannedBy, &b.ExpiresAt, &b.CreatedAt); err != nil {
			return nil, err
		}
		bans = append(bans, b)
	}
	return bans, rows.Err()
}

// AddMute records (or refreshes) a mute of userID in roomID by mutedBy.
func (r *Repository) AddMute(ctx context.Context, roomID, userID, mutedBy uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO room_mutes (room_id, user_id, muted_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (room_id, user_id) DO UPDATE SET
			muted_by = EXCLUDED.muted_by,
			created_at = NOW()
	`, roomID, userID, mutedBy)
	return err
}

// RemoveMute lifts any mute of userID in roomID, reporting whether a row was removed.
func (r *Repository) RemoveMute(ctx context.Context, roomID, userID uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM room_mutes WHERE room_id = $1 AND user_id = $2`, roomID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListMutes returns the mutes in roomID, newest first.
func (r *Repository) ListMutes(ctx context.Context, roomID uuid.UUID) ([]Mute, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT room_id, user_id, muted_by, created_at
		FROM room_mutes
		WHERE room_id = $1
		ORDER BY created_at DESC
	`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mutes []Mute
	for rows.Next() {
		var m Mute
		if err := rows.Scan(&m.RoomID, &m.UserID, &m.MutedBy, &m.CreatedAt); err != nil {
			return nil, err
		}
		mutes = append(mutes, m)
	}
	return mutes, rows.Err()
}
