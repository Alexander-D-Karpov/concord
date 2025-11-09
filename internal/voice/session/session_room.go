package session

import (
	"sync"
	"time"
)

type VoiceSession struct {
	UserID       string
	RoomID       string
	ServerID     string
	Muted        bool
	VideoEnabled bool
	Speaking     bool
	JoinedAt     time.Time
}

type RoomManager struct {
	mu       sync.RWMutex
	sessions map[string]map[string]*VoiceSession
}

func NewRoomManager() *RoomManager {
	return &RoomManager{
		sessions: make(map[string]map[string]*VoiceSession),
	}
}

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

func (m *RoomManager) GetSession(roomID, userID string) *VoiceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if room, exists := m.sessions[roomID]; exists {
		return room[userID]
	}
	return nil
}

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

func (m *RoomManager) GetAllRooms() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rooms := make([]string, 0, len(m.sessions))
	for roomID := range m.sessions {
		rooms = append(rooms, roomID)
	}
	return rooms
}

func (m *RoomManager) GetTotalParticipants() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := 0
	for _, room := range m.sessions {
		total += len(room)
	}
	return total
}

func (m *RoomManager) IsUserInRoom(roomID, userID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if room, exists := m.sessions[roomID]; exists {
		_, ok := room[userID]
		return ok
	}
	return false
}
