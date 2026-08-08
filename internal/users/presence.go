package users

import (
	"context"
	"sync"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// StatusOnline marks a user as actively connected.
	StatusOnline = "online"
	// StatusAway marks a user as idle but still connected.
	StatusAway = "away"
	// StatusOffline marks a user as not connected (the default when unknown).
	StatusOffline = "offline"
	// StatusDND ("do not disturb") is a user-chosen preference suppressing notifications.
	StatusDND = "dnd"
)

// PresenceManager tracks each user's live presence in an in-memory map guarded by
// mu. State is per-process and lost on restart (it is not persisted to Redis);
// transitions broadcast the user's effective status to their friends and shared rooms.
type PresenceManager struct {
	mu             sync.RWMutex
	currentState   map[uuid.UUID]string
	pendingOffline map[uuid.UUID]time.Time
	repo           *Repository
	hub            *events.Hub
}

// NewPresenceManager returns a PresenceManager with an empty in-memory state map,
// using repo for status-preference/friend lookups and hub for broadcasting changes.
func NewPresenceManager(repo *Repository, hub *events.Hub) *PresenceManager {
	return &PresenceManager{
		currentState:   make(map[uuid.UUID]string),
		pendingOffline: make(map[uuid.UUID]time.Time),
		repo:           repo,
		hub:            hub,
	}
}

// markDisconnectedAt records the user as away and schedules a pending offline at
// now. Pure map bookkeeping; the exported MarkDisconnected also broadcasts.
func (pm *PresenceManager) markDisconnectedAt(userID uuid.UUID, now time.Time) {
	pm.currentState[userID] = StatusAway
	pm.pendingOffline[userID] = now
}

// clearPending cancels a scheduled offline (e.g. the user reconnected).
func (pm *PresenceManager) clearPending(userID uuid.UUID) {
	delete(pm.pendingOffline, userID)
}

// expiredGrace returns users whose pending offline is older than grace as of now.
func (pm *PresenceManager) expiredGrace(now time.Time, grace time.Duration) []uuid.UUID {
	var out []uuid.UUID
	for id, at := range pm.pendingOffline {
		if now.Sub(at) >= grace {
			out = append(out, id)
		}
	}
	return out
}

// Heartbeat marks the user online; it is an alias for SetOnline intended for
// periodic keep-alive pings from a connected client.
func (pm *PresenceManager) Heartbeat(ctx context.Context, userID uuid.UUID) error {
	return pm.SetOnline(ctx, userID)
}

// SetOnline records the user as online in the in-memory map and, only if the state
// actually changed, broadcasts the new effective status to friends and shared rooms.
func (pm *PresenceManager) SetOnline(ctx context.Context, userID uuid.UUID) error {
	pm.mu.Lock()
	oldState := pm.currentState[userID]
	pm.currentState[userID] = StatusOnline
	delete(pm.pendingOffline, userID)
	pm.mu.Unlock()

	if oldState != StatusOnline {
		pm.broadcastCurrentStatus(ctx, userID)
	}
	return nil
}

// SetAway records the user as away in the in-memory map and, only if the state
// actually changed, broadcasts the new effective status to friends and shared rooms.
func (pm *PresenceManager) SetAway(ctx context.Context, userID uuid.UUID) error {
	pm.mu.Lock()
	oldState := pm.currentState[userID]
	pm.currentState[userID] = StatusAway
	pm.mu.Unlock()

	if oldState != StatusAway {
		pm.broadcastCurrentStatus(ctx, userID)
	}
	return nil
}

// SetOffline records the user as offline in the in-memory map and, only if the
// state actually changed, broadcasts the new effective status to friends and shared rooms.
func (pm *PresenceManager) SetOffline(ctx context.Context, userID uuid.UUID) error {
	pm.mu.Lock()
	oldState := pm.currentState[userID]
	pm.currentState[userID] = StatusOffline
	pm.mu.Unlock()

	if oldState != StatusOffline {
		pm.broadcastCurrentStatus(ctx, userID)
	}
	return nil
}

// MarkDisconnected marks the user away and schedules them to go offline after the
// grace period (see ReapOfflineGrace), broadcasting the away transition. Called when
// a client's stream drops, so a backgrounded phone does not flap straight to offline.
func (pm *PresenceManager) MarkDisconnected(ctx context.Context, userID uuid.UUID) error {
	pm.mu.Lock()
	pm.markDisconnectedAt(userID, time.Now())
	pm.mu.Unlock()
	pm.broadcastCurrentStatus(ctx, userID)
	return nil
}

// ReapOfflineGrace transitions to offline every user whose disconnect grace has
// elapsed, returning how many were reaped. Intended to be called periodically.
func (pm *PresenceManager) ReapOfflineGrace(ctx context.Context, grace time.Duration) int {
	pm.mu.Lock()
	expired := pm.expiredGrace(time.Now(), grace)
	for _, id := range expired {
		delete(pm.pendingOffline, id)
	}
	pm.mu.Unlock()
	for _, id := range expired {
		_ = pm.SetOffline(ctx, id)
	}
	return len(expired)
}

// Stop is a no-op; it exists to satisfy a lifecycle interface since presence is
// held in-memory with no background goroutines to shut down.
func (pm *PresenceManager) Stop() {}

// GetStatus returns the user's current in-memory presence, or StatusOffline if the
// user has no recorded state.
func (pm *PresenceManager) GetStatus(userID uuid.UUID) string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if s, ok := pm.currentState[userID]; ok {
		return s
	}
	return StatusOffline
}

// broadcastCurrentStatus invalidates the user's cached record, computes their
// effective status by combining the stored preference with live presence, and
// emits a UserStatusChanged event to each friend and each shared room. A no-op if
// no hub is configured; a failed preference lookup falls back to StatusOnline.
func (pm *PresenceManager) broadcastCurrentStatus(ctx context.Context, userID uuid.UUID) {
	if pm.hub == nil {
		return
	}

	_ = pm.repo.InvalidateCache(ctx, userID)

	statusPreference, err := pm.repo.GetStatusPreference(ctx, userID)
	if err != nil {
		statusPreference = StatusOnline
	}

	effective := EffectiveStatus(statusPreference, pm.GetStatus(userID))

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_UserStatusChanged{
			UserStatusChanged: &streamv1.UserStatusChanged{
				UserId: userID.String(),
				Status: effective,
			},
		},
	}

	friends, _ := pm.getFriendsList(ctx, userID)
	for _, friendID := range friends {
		pm.hub.BroadcastToUser(friendID.String(), event)
	}

	query := `SELECT room_id FROM memberships WHERE user_id = $1`
	rows, err := pm.repo.pool.Query(ctx, query, userID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var roomID uuid.UUID
			if err := rows.Scan(&roomID); err == nil {
				pm.hub.BroadcastToRoom(roomID.String(), event)
			}
		}
	}
}

// getFriendsList returns the user IDs of the user's friends, resolving each
// friendship's ordered pair to the other participant.
func (pm *PresenceManager) getFriendsList(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	query := `
		SELECT CASE WHEN user_id1 = $1 THEN user_id2 ELSE user_id1 END
		FROM friendships WHERE user_id1 = $1 OR user_id2 = $1
	`
	rows, err := pm.repo.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var friends []uuid.UUID
	for rows.Next() {
		var fid uuid.UUID
		if err := rows.Scan(&fid); err != nil {
			return nil, err
		}
		friends = append(friends, fid)
	}
	return friends, rows.Err()
}

// Refresh re-broadcasts the user's current effective status without changing the
// stored state, e.g. after a status-preference update.
func (pm *PresenceManager) Refresh(ctx context.Context, userID uuid.UUID) {
	pm.broadcastCurrentStatus(ctx, userID)
}
