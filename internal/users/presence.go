package users

import (
	"context"
	"sync"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	StatusOnline  = "online"
	StatusAway    = "away"
	StatusOffline = "offline"
	StatusDND     = "dnd"
)

type PresenceManager struct {
	mu           sync.RWMutex
	currentState map[uuid.UUID]string
	repo         *Repository
	hub          *events.Hub
}

func NewPresenceManager(repo *Repository, hub *events.Hub) *PresenceManager {
	return &PresenceManager{
		currentState: make(map[uuid.UUID]string),
		repo:         repo,
		hub:          hub,
	}
}

func (pm *PresenceManager) Heartbeat(ctx context.Context, userID uuid.UUID) error {
	return pm.SetOnline(ctx, userID)
}

func (pm *PresenceManager) SetOnline(ctx context.Context, userID uuid.UUID) error {
	pm.mu.Lock()
	oldState := pm.currentState[userID]
	pm.currentState[userID] = StatusOnline
	pm.mu.Unlock()

	if oldState != StatusOnline {
		pm.broadcastCurrentStatus(ctx, userID)
	}
	return nil
}

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

func (pm *PresenceManager) Stop() {}

func (pm *PresenceManager) GetStatus(userID uuid.UUID) string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if s, ok := pm.currentState[userID]; ok {
		return s
	}
	return StatusOffline
}

func (pm *PresenceManager) broadcastCurrentStatus(ctx context.Context, userID uuid.UUID) {
	if pm.hub == nil {
		return
	}

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

func (pm *PresenceManager) Refresh(ctx context.Context, userID uuid.UUID) {
	pm.broadcastCurrentStatus(ctx, userID)
}
