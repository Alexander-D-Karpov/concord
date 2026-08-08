package session

import (
	"sync"
	"time"
)

// VoiceSession is a lightweight, control-plane view of a participant's presence in
// a room (identity, assigned voice ServerID, and mute/video/speaking flags),
// distinct from the data-plane Session that carries media/crypto state.
type VoiceSession struct {
	UserID       string
	RoomID       string
	ServerID     string
	Muted        bool
	VideoEnabled bool
	Speaking     bool
	JoinedAt     time.Time
}

// RoomManager tracks room membership as a room -> user -> VoiceSession map, guarded
// by mu. It is the presence/roster registry, separate from the media Manager.
type RoomManager struct {
	mu       sync.RWMutex
	sessions map[string]map[string]*VoiceSession
}

// NewRoomManager returns an empty RoomManager.
func NewRoomManager() *RoomManager {
	return &RoomManager{
		sessions: make(map[string]map[string]*VoiceSession),
	}
}

// AddSession adds (or overwrites) a user's presence in a room, creating the room
// bucket if needed. VideoEnabled starts as !audioOnly; muted/speaking start false.
func (m *RoomManager) AddSession(roomID, userID, serverID string, audioOnly bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[roomID]; !exists {
		m.sessions[roomID] = make(map[string]*VoiceSession)
	}

	m.sessions[roomID][userID] = &VoiceSession{
		UserID:       userID,
		RoomID:       roomID,
		ServerID:     serverID,
		Muted:        false,
		VideoEnabled: !audioOnly,
		Speaking:     false,
		JoinedAt:     time.Now(),
	}
}

// RemoveSession removes a user from a room and drops the room bucket once empty.
// No-op if the room or user is absent.
func (m *RoomManager) RemoveSession(roomID, userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if room, exists := m.sessions[roomID]; exists {
		delete(room, userID)
		if len(room) == 0 {
			delete(m.sessions, roomID)
		}
	}
}

// GetRoomSessions returns a snapshot slice of the room's presences (empty, non-nil
// slice for an unknown room), in unspecified order.
func (m *RoomManager) GetRoomSessions(roomID string) []*VoiceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	room, exists := m.sessions[roomID]
	if !exists {
		return []*VoiceSession{}
	}

	sessions := make([]*VoiceSession, 0, len(room))
	for _, session := range room {
		sessions = append(sessions, session)
	}
	return sessions
}

// GetSession returns the user's presence in a room, or nil if not present. The
// pointer aliases stored state; treat as read-only.
func (m *RoomManager) GetSession(roomID, userID string) *VoiceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if room, exists := m.sessions[roomID]; exists {
		return room[userID]
	}
	return nil
}

// UpdateMuted sets the user's muted flag, returning false if the user is not in the
// room.
func (m *RoomManager) UpdateMuted(roomID, userID string, muted bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if room, exists := m.sessions[roomID]; exists {
		if session, ok := room[userID]; ok {
			session.Muted = muted
			return true
		}
	}
	return false
}

// UpdateVideoEnabled sets the user's video flag, returning false if the user is not
// in the room.
func (m *RoomManager) UpdateVideoEnabled(roomID, userID string, enabled bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if room, exists := m.sessions[roomID]; exists {
		if session, ok := room[userID]; ok {
			session.VideoEnabled = enabled
			return true
		}
	}
	return false
}

// UpdateSpeaking sets the user's speaking flag, returning false if the user is not
// in the room.
func (m *RoomManager) UpdateSpeaking(roomID, userID string, speaking bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if room, exists := m.sessions[roomID]; exists {
		if session, ok := room[userID]; ok {
			session.Speaking = speaking
			return true
		}
	}
	return false
}

// GetAllRooms returns the IDs of all non-empty rooms, in unspecified order.
func (m *RoomManager) GetAllRooms() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rooms := make([]string, 0, len(m.sessions))
	for roomID := range m.sessions {
		rooms = append(rooms, roomID)
	}
	return rooms
}

// GetTotalParticipants returns the count of presences across all rooms.
func (m *RoomManager) GetTotalParticipants() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := 0
	for _, room := range m.sessions {
		total += len(room)
	}
	return total
}

// IsUserInRoom reports whether userID currently has a presence in roomID.
func (m *RoomManager) IsUserInRoom(roomID, userID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if room, exists := m.sessions[roomID]; exists {
		_, ok := room[userID]
		return ok
	}
	return false
}
