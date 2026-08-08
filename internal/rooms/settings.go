package rooms

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// settingsCacheTTL bounds how long a cached RoomSettings may be stale with respect
// to changes made outside UpdateSettings (e.g. is_private via UpdateRoom).
const settingsCacheTTL = 30 * time.Second

// settingsCacheKey is the cache key for a room's settings.
func settingsCacheKey(roomID uuid.UUID) string {
	return fmt.Sprintf("room:settings:%s", roomID)
}

// RoomSettings is the full set of per-room settings. SlowModeInterval and IsPrivate
// are stored on the rooms table (authoritative there); the rest live in
// room_settings, and WordFilters in room_word_filters.
type RoomSettings struct {
	SlowModeInterval    int
	WhoCanInvite        string // "member" | "moderator"
	WhoCanPost          string // "member" | "moderator"
	IsPrivate           bool
	RequireApproval     bool // advisory
	MemberCap           int  // 0 = unlimited
	RetentionDays       int  // 0 = keep forever
	LinkPreviewsEnabled bool // advisory
	GifsEnabled         bool // advisory
	StickersEnabled     bool // advisory
	WordFilters         []string
}

// defaultRoomSettings returns the settings a room has before any UpdateSettings
// call (matching the room_settings column defaults).
func defaultRoomSettings() RoomSettings {
	return RoomSettings{
		WhoCanInvite:        "member",
		WhoCanPost:          "member",
		LinkPreviewsEnabled: true,
		GifsEnabled:         true,
		StickersEnabled:     true,
	}
}

// GetSettings returns the room's effective settings: is_private and
// slow_mode_interval come from the rooms table, the remaining fields from
// room_settings (or defaults when no row exists), and the word list from
// room_word_filters.
func (r *Repository) GetSettings(ctx context.Context, roomID uuid.UUID) (RoomSettings, error) {
	if r.cache != nil {
		var cached RoomSettings
		if err := r.cache.Get(ctx, settingsCacheKey(roomID), &cached); err == nil {
			return cached, nil
		}
	}

	s := defaultRoomSettings()

	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(is_private, false), COALESCE(slow_mode_interval, 0) FROM rooms WHERE id = $1`,
		roomID,
	).Scan(&s.IsPrivate, &s.SlowModeInterval); err != nil {
		return RoomSettings{}, err
	}

	err := r.pool.QueryRow(ctx, `
		SELECT who_can_invite, who_can_post, require_approval, member_cap, retention_days,
		       link_previews_enabled, gifs_enabled, stickers_enabled
		FROM room_settings WHERE room_id = $1
	`, roomID).Scan(
		&s.WhoCanInvite, &s.WhoCanPost, &s.RequireApproval, &s.MemberCap, &s.RetentionDays,
		&s.LinkPreviewsEnabled, &s.GifsEnabled, &s.StickersEnabled,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return RoomSettings{}, err
	}

	filters, err := r.ListWordFilters(ctx, roomID)
	if err != nil {
		return RoomSettings{}, err
	}
	s.WordFilters = filters

	if r.cache != nil {
		_ = r.cache.Set(ctx, settingsCacheKey(roomID), s, settingsCacheTTL)
	}
	return s, nil
}

// UpdateSettings persists s for the room in a single transaction: is_private and
// slow_mode_interval are written to the rooms table, the remaining scalar settings
// are upserted into room_settings, and the word-filter set is fully replaced.
func (r *Repository) UpdateSettings(ctx context.Context, roomID uuid.UUID, s RoomSettings) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE rooms SET is_private = $2, slow_mode_interval = $3 WHERE id = $1`,
		roomID, s.IsPrivate, s.SlowModeInterval,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO room_settings
			(room_id, who_can_invite, who_can_post, require_approval, member_cap, retention_days,
			 link_previews_enabled, gifs_enabled, stickers_enabled, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		ON CONFLICT (room_id) DO UPDATE SET
			who_can_invite = EXCLUDED.who_can_invite,
			who_can_post = EXCLUDED.who_can_post,
			require_approval = EXCLUDED.require_approval,
			member_cap = EXCLUDED.member_cap,
			retention_days = EXCLUDED.retention_days,
			link_previews_enabled = EXCLUDED.link_previews_enabled,
			gifs_enabled = EXCLUDED.gifs_enabled,
			stickers_enabled = EXCLUDED.stickers_enabled,
			updated_at = NOW()
	`, roomID, s.WhoCanInvite, s.WhoCanPost, s.RequireApproval, s.MemberCap, s.RetentionDays,
		s.LinkPreviewsEnabled, s.GifsEnabled, s.StickersEnabled,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM room_word_filters WHERE room_id = $1`, roomID); err != nil {
		return err
	}
	for _, w := range s.WordFilters {
		if w == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO room_word_filters (room_id, word) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			roomID, w,
		); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if r.cache != nil {
		_ = r.cache.Delete(ctx, settingsCacheKey(roomID))
	}
	return nil
}

// ListWordFilters returns the room's configured filter words.
func (r *Repository) ListWordFilters(ctx context.Context, roomID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT word FROM room_word_filters WHERE room_id = $1 ORDER BY word`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var words []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		words = append(words, w)
	}
	return words, rows.Err()
}

// SetMemberRole updates a member's role in a room and invalidates the member and
// member-list caches (so a running server sees the new role immediately). Returns
// false if the user is not a member of the room. Valid roles are enforced by the
// caller / the membership_role enum.
func (r *Repository) SetMemberRole(ctx context.Context, roomID, userID uuid.UUID, role string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE memberships SET role = $3 WHERE room_id = $1 AND user_id = $2`, roomID, userID, role)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if r.cache != nil {
		_ = r.cache.Delete(ctx, r.memberCacheKey(roomID, userID), r.memberListCacheKey(roomID))
	}
	return true, nil
}

// CountMembers returns the number of members in roomID, used for member-cap
// enforcement.
func (r *Repository) CountMembers(ctx context.Context, roomID uuid.UUID) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM memberships WHERE room_id = $1`, roomID).Scan(&n)
	return n, err
}

// AddMemberIfBelowCap atomically adds userID to roomID only if the room currently
// has fewer than cap members, returning whether the member was added (false means
// the cap was reached). It takes a FOR UPDATE lock on the room row so concurrent
// joins serialize and cannot both slip past the cap. cap must be > 0; use AddMember
// for unlimited rooms. Re-adding an existing member is a no-op that returns true.
func (r *Repository) AddMemberIfBelowCap(ctx context.Context, roomID, userID uuid.UUID, role string, cap int) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the room row so concurrent AddMemberIfBelowCap calls for this room run
	// one at a time, making the count-then-insert atomic.
	var locked uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM rooms WHERE id = $1 FOR UPDATE`, roomID).Scan(&locked); err != nil {
		return false, err
	}

	// An already-present member is fine (idempotent) and does not consume a slot.
	var alreadyMember bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM memberships WHERE room_id = $1 AND user_id = $2)`, roomID, userID,
	).Scan(&alreadyMember); err != nil {
		return false, err
	}
	if !alreadyMember {
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM memberships WHERE room_id = $1`, roomID).Scan(&count); err != nil {
			return false, err
		}
		if count >= cap {
			return false, tx.Commit(ctx)
		}
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO memberships (room_id, user_id, role) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		roomID, userID, role,
	); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
