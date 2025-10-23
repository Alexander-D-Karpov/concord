package session

import (
	"net"
	"sync"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
)

type Session struct {
	ID           uint32
	UserID       string
	RoomID       string
	SSRC         uint32
	Addr         *net.UDPAddr
	Cipher       *crypto.Cipher
	LastActivity time.Time
	Muted        bool
	VideoEnabled bool
	Speaking     bool
	mu           sync.RWMutex
}

type Manager struct {
	mu       sync.RWMutex
	sessions map[uint32]*Session
	userMap  map[string]*Session
	roomMap  map[string]map[uint32]*Session
	nextSSRC uint32
}

func NewManager() *Manager {
	return &Manager{
		sessions: make(map[uint32]*Session),
		userMap:  make(map[string]*Session),
		roomMap:  make(map[string]map[uint32]*Session),
		nextSSRC: 1000,
	}
}

func (m *Manager) CreateSession(userID, roomID string, addr *net.UDPAddr, cipher *crypto.Cipher) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessionID := m.nextSSRC
	m.nextSSRC++

	ssrc := m.nextSSRC
	m.nextSSRC++

	session := &Session{
		ID:           sessionID,
		UserID:       userID,
		RoomID:       roomID,
		SSRC:         ssrc,
		Addr:         addr,
		Cipher:       cipher,
		LastActivity: time.Now(),
		Muted:        false,
		VideoEnabled: false,
		Speaking:     false,
	}

	m.sessions[sessionID] = session
	m.userMap[userID] = session

	if m.roomMap[roomID] == nil {
		m.roomMap[roomID] = make(map[uint32]*Session)
	}
	m.roomMap[roomID][sessionID] = session

	return session
}

func (m *Manager) GetSession(sessionID uint32) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

func (m *Manager) GetSessionByUser(userID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.userMap[userID]
}

func (m *Manager) GetRoomSessions(roomID string) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	roomSessions := m.roomMap[roomID]
	sessions := make([]*Session, 0, len(roomSessions))
	for _, session := range roomSessions {
		sessions = append(sessions, session)
	}
	return sessions
}

func (m *Manager) GetAllSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

func (m *Manager) RemoveSession(sessionID uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[sessionID]
	if !ok {
		return
	}

	delete(m.sessions, sessionID)
	delete(m.userMap, session.UserID)

	if roomSessions, exists := m.roomMap[session.RoomID]; exists {
		delete(roomSessions, sessionID)
		if len(roomSessions) == 0 {
			delete(m.roomMap, session.RoomID)
		}
	}
}

func (m *Manager) UpdateActivity(sessionID uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if session, ok := m.sessions[sessionID]; ok {
		session.LastActivity = time.Now()
	}
}

func (m *Manager) CleanupInactive(timeout time.Duration) []uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var removed []uint32

	for sessionID, session := range m.sessions {
		if now.Sub(session.LastActivity) > timeout {
			removed = append(removed, sessionID)
			delete(m.sessions, sessionID)
			delete(m.userMap, session.UserID)

			if roomSessions, exists := m.roomMap[session.RoomID]; exists {
				delete(roomSessions, sessionID)
				if len(roomSessions) == 0 {
					delete(m.roomMap, session.RoomID)
				}
			}
		}
	}

	return removed
}

func (s *Session) UpdateAddr(addr *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Addr = addr
	s.LastActivity = time.Now()
}

func (s *Session) SetMuted(muted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Muted = muted
}

func (s *Session) SetVideoEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.VideoEnabled = enabled
}

func (s *Session) SetSpeaking(speaking bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Speaking = speaking
}

func (s *Session) GetAddr() *net.UDPAddr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Addr
}
