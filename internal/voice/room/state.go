package room

import (
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
)

// State is one room's session set keyed by session ID (SSRC), guarded by its own
// RWMutex. Part of the legacy room package (see the package doc); the live server
// tracks rooms in session.Manager.
type State struct {
	ID       string
	Sessions map[uint32]*session.Session
	mu       sync.RWMutex
}

// Manager is the legacy in-memory registry of rooms by ID. Only GetAllRooms is
// exercised in the running server (for a health check); see the package doc.
type Manager struct {
	rooms map[string]*State
	mu    sync.RWMutex
}

// NewManager returns an empty room Manager.
func NewManager() *Manager {
	return &Manager{
		rooms: make(map[string]*State),
	}
}

// GetOrCreateRoom returns the room for roomID, creating and registering an empty
// one if absent. Concurrency-safe; the returned *State is shared, so mutate it
// only through its own methods.
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

// GetRoom returns the room for roomID, or nil if it does not exist (unlike
// GetOrCreateRoom, it never creates one).
func (m *Manager) GetRoom(roomID string) *State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rooms[roomID]
}

// RemoveRoom deletes roomID from the registry; a no-op if absent.
func (m *Manager) RemoveRoom(roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rooms, roomID)
}

// GetAllRooms returns a snapshot slice of the current rooms in unspecified order.
// The slice is freshly allocated but the *State elements are shared.
func (m *Manager) GetAllRooms() []*State {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rooms := make([]*State, 0, len(m.rooms))
	for _, room := range m.rooms {
		rooms = append(rooms, room)
	}
	return rooms
}

// AddSession adds or replaces the session under its ID (SSRC). Concurrency-safe.
func (r *State) AddSession(sess *session.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Sessions[sess.ID] = sess
}

// RemoveSession deletes the session with the given ID; a no-op if absent.
func (r *State) RemoveSession(sessionID uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Sessions, sessionID)
}

// GetSessions returns a snapshot slice of the room's sessions in unspecified
// order; the slice is fresh but the elements are shared.
func (r *State) GetSessions() []*session.Session {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sessions := make([]*session.Session, 0, len(r.Sessions))
	for _, sess := range r.Sessions {
		sessions = append(sessions, sess)
	}
	return sessions
}

// GetSessionCount returns the number of sessions currently in the room.
func (r *State) GetSessionCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.Sessions)
}
