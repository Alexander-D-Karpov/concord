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
	StatusOnline  = "online"
	StatusAway    = "away"
	StatusOffline = "offline"

	HeartbeatInterval = 30 * time.Second
	AwayThreshold     = 5 * time.Minute
	OfflineThreshold  = 10 * time.Minute
)

type PresenceManager struct {
	mu           sync.RWMutex
	lastActivity map[uuid.UUID]time.Time
	currentState map[uuid.UUID]string
	repo         *Repository
	hub          *events.Hub
	stopCh       chan struct{}
}

func NewPresenceManager(repo *Repository, hub *events.Hub) *PresenceManager {
	pm := &PresenceManager{
		lastActivity: make(map[uuid.UUID]time.Time),
		currentState: make(map[uuid.UUID]string),
		repo:         repo,
		hub:          hub,
		stopCh:       make(chan struct{}),
	}
	go pm.runChecker()
	return pm
}

func (pm *PresenceManager) Heartbeat(ctx context.Context, userID uuid.UUID) error {
	pm.mu.Lock()
	pm.lastActivity[userID] = time.Now()
	oldState := pm.currentState[userID]
	pm.currentState[userID] = StatusOnline
	pm.mu.Unlock()

	if oldState != StatusOnline {
		if err := pm.repo.UpdateStatus(ctx, userID, StatusOnline); err != nil {
			return err
		}
		pm.broadcastStatusChange(ctx, userID, StatusOnline)
	}

	return nil
}

func (pm *PresenceManager) SetOffline(ctx context.Context, userID uuid.UUID) error {
	pm.mu.Lock()
	delete(pm.lastActivity, userID)
	pm.currentState[userID] = StatusOffline
	pm.mu.Unlock()

	if err := pm.repo.UpdateStatus(ctx, userID, StatusOffline); err != nil {
		return err
	}
	pm.broadcastStatusChange(ctx, userID, StatusOffline)
	return nil
}

func (pm *PresenceManager) runChecker() {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pm.checkPresence()
		case <-pm.stopCh:
			return
		}
	}
}

func (pm *PresenceManager) checkPresence() {
	now := time.Now()
	ctx := context.Background()

	pm.mu.Lock()
	var updates []struct {
		userID   uuid.UUID
		newState string
	}

	for userID, lastActive := range pm.lastActivity {
		idle := now.Sub(lastActive)
		currentState := pm.currentState[userID]
		var newState string

		if idle > OfflineThreshold {
			newState = StatusOffline
			delete(pm.lastActivity, userID)
		} else if idle > AwayThreshold {
			newState = StatusAway
		} else {
			newState = StatusOnline
		}

		if newState != currentState {
			pm.currentState[userID] = newState
			updates = append(updates, struct {
				userID   uuid.UUID
				newState string
			}{userID, newState})
		}
	}
	pm.mu.Unlock()

	for _, update := range updates {
		_ = pm.repo.UpdateStatus(ctx, update.userID, update.newState)
		pm.broadcastStatusChange(ctx, update.userID, update.newState)
	}
}

func (pm *PresenceManager) broadcastStatusChange(ctx context.Context, userID uuid.UUID, status string) {
	if pm.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_UserStatusChanged{
			UserStatusChanged: &streamv1.UserStatusChanged{
				UserId: userID.String(),
				Status: status,
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

func (pm *PresenceManager) Stop() {
	close(pm.stopCh)
}

func (pm *PresenceManager) GetStatus(userID uuid.UUID) string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if s, ok := pm.currentState[userID]; ok {
		return s
	}
	return StatusOffline
}
