package room

import (
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
)

type State struct {
	ID       string
	Sessions map[uint32]*session.Session
	mu       sync.RWMutex
}

type Manager struct {
	rooms map[string]*State
	mu    sync.RWMutex
}

func NewManager() *Manager {
	return &Manager{
		rooms: make(map[string]*State),
	}
}

func (m *Manager) GetOrCreateRoom(roomID string) *State {
	m.mu.Lock()
	defer m.mu.Unlock()

	if room, exists := m.rooms[roomID]; exists {
		return room
	}

	room := &State{
		ID:       roomID,
		Sessions: make(map[uint32]*session.Session),
	}
	m.rooms[roomID] = room
	return room
}

func (m *Manager) GetRoom(roomID string) *State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rooms[roomID]
}

func (m *Manager) RemoveRoom(roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rooms, roomID)
}

func (m *Manager) GetAllRooms() []*State {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rooms := make([]*State, 0, len(m.rooms))
	for _, room := range m.rooms {
		rooms = append(rooms, room)
	}
	return rooms
}

func (r *State) AddSession(sess *session.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Sessions[sess.ID] = sess
}

func (r *State) RemoveSession(sessionID uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Sessions, sessionID)
}

func (r *State) GetSessions() []*session.Session {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sessions := make([]*session.Session, 0, len(r.Sessions))
	for _, sess := range r.Sessions {
		sessions = append(sessions, sess)
	}
	return sessions
}

func (r *State) GetSessionCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.Sessions)
}
