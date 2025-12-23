package session

import (
	"net"
	"sync"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
)

type Session struct {
	ID        uint32
	UserID    string
	RoomID    string
	SSRC      uint32
	VideoSSRC uint32

	addr         *net.UDPAddr
	lastActivity time.Time

	Crypto       *crypto.SessionCrypto
	Muted        bool
	VideoEnabled bool
	Speaking     bool

	mu sync.RWMutex

	AudioSeq uint16
	VideoSeq uint16
	AudioTS  uint32
	VideoTS  uint32

	retransmitBuf *RetransmitBuffer
}

func (s *Session) GetAddr() *net.UDPAddr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.addr
}

func (s *Session) LastActivity() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastActivity
}

func (s *Session) touchLocked(now time.Time) {
	s.lastActivity = now
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

func (s *Session) NextAudioSeq() uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := s.AudioSeq
	s.AudioSeq++
	return seq
}

func (s *Session) NextVideoSeq() uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := s.VideoSeq
	s.VideoSeq++
	return seq
}

func (s *Session) StoreForRetransmit(seq uint16, data []byte) {
	if s.retransmitBuf != nil {
		s.retransmitBuf.Store(seq, data)
	}
}

func (s *Session) GetForRetransmit(seq uint16) []byte {
	if s.retransmitBuf != nil {
		return s.retransmitBuf.Get(seq)
	}
	return nil
}

type RetransmitBuffer struct {
	mu      sync.RWMutex
	packets map[uint16]*CachedPacket
	maxAge  time.Duration
}

type CachedPacket struct {
	Data      []byte
	Timestamp time.Time
	Sequence  uint16
}

func NewRetransmitBuffer(maxAge time.Duration) *RetransmitBuffer {
	return &RetransmitBuffer{
		packets: make(map[uint16]*CachedPacket),
		maxAge:  maxAge,
	}
}

func (rb *RetransmitBuffer) Store(seq uint16, data []byte) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	now := time.Now()
	rb.packets[seq] = &CachedPacket{
		Data:      dataCopy,
		Timestamp: now,
		Sequence:  seq,
	}

	for s, p := range rb.packets {
		if now.Sub(p.Timestamp) > rb.maxAge {
			delete(rb.packets, s)
		}
	}
}

func (rb *RetransmitBuffer) Get(seq uint16) []byte {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if p, ok := rb.packets[seq]; ok {
		if time.Since(p.Timestamp) <= rb.maxAge {
			return p.Data
		}
	}
	return nil
}

type Manager struct {
	mu sync.RWMutex

	sessions map[uint32]*Session            // sessionID -> session
	userRoom map[string]*Session            // "user:room" -> session
	roomMap  map[string]map[uint32]*Session // roomID -> sessionID -> session
	ssrcMap  map[uint32]*Session            // SSRC -> session
	addrMap  map[string]*Session            // "ip:port" -> session

	nextID   uint32
	nextSSRC uint32
}

func NewManager() *Manager {
	return &Manager{
		sessions: make(map[uint32]*Session),
		userRoom: make(map[string]*Session),
		roomMap:  make(map[string]map[uint32]*Session),
		ssrcMap:  make(map[uint32]*Session),
		addrMap:  make(map[string]*Session),
		nextID:   1000,
		nextSSRC: 2000,
	}
}

func userRoomKey(userID, roomID string) string { return userID + ":" + roomID }

func (m *Manager) CreateSession(
	userID, roomID string,
	addr *net.UDPAddr,
	sessionCrypto *crypto.SessionCrypto,
	videoEnabled bool,
) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	sessionID := m.nextID
	m.nextID++

	audioSSRC := m.nextSSRC
	m.nextSSRC++

	var videoSSRC uint32
	if videoEnabled {
		videoSSRC = m.nextSSRC
		m.nextSSRC++
	}

	sess := &Session{
		ID:            sessionID,
		UserID:        userID,
		RoomID:        roomID,
		SSRC:          audioSSRC,
		VideoSSRC:     videoSSRC,
		addr:          addr,
		lastActivity:  now,
		Crypto:        sessionCrypto,
		Muted:         false,
		VideoEnabled:  videoEnabled,
		Speaking:      false,
		retransmitBuf: NewRetransmitBuffer(500 * time.Millisecond),
	}

	m.sessions[sessionID] = sess
	m.userRoom[userRoomKey(userID, roomID)] = sess

	if m.roomMap[roomID] == nil {
		m.roomMap[roomID] = make(map[uint32]*Session)
	}
	m.roomMap[roomID][sessionID] = sess

	m.ssrcMap[audioSSRC] = sess
	if videoSSRC != 0 {
		m.ssrcMap[videoSSRC] = sess
	}

	if addr != nil {
		m.addrMap[addr.String()] = sess
	}

	return sess
}

func (m *Manager) BindAddr(sessionID uint32, addr *net.UDPAddr) {
	if addr == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	sess := m.sessions[sessionID]
	if sess == nil {
		return
	}

	old := sess.GetAddr()
	if old != nil {
		delete(m.addrMap, old.String())
	}

	now := time.Now()
	sess.mu.Lock()
	sess.addr = addr
	sess.touchLocked(now)
	sess.mu.Unlock()

	m.addrMap[addr.String()] = sess
}

func (m *Manager) Touch(sessionID uint32) {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return
	}
	now := time.Now()
	sess.mu.Lock()
	sess.touchLocked(now)
	sess.mu.Unlock()
}

func (m *Manager) GetSession(sessionID uint32) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

func (m *Manager) GetBySSRC(ssrc uint32) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ssrcMap[ssrc]
}

func (m *Manager) GetByAddr(addr *net.UDPAddr) *Session {
	if addr == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.addrMap[addr.String()]
}

func (m *Manager) GetSessionByUserInRoom(userID, roomID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.userRoom[userRoomKey(userID, roomID)]
}

func (m *Manager) GetRoomSessions(roomID string) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	roomSessions := m.roomMap[roomID]
	out := make([]*Session, 0, len(roomSessions))
	for _, s := range roomSessions {
		out = append(out, s)
	}
	return out
}

func (m *Manager) RemoveSession(sessionID uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess := m.sessions[sessionID]
	if sess == nil {
		return
	}

	delete(m.sessions, sessionID)
	delete(m.userRoom, userRoomKey(sess.UserID, sess.RoomID))

	delete(m.ssrcMap, sess.SSRC)
	if sess.VideoSSRC != 0 {
		delete(m.ssrcMap, sess.VideoSSRC)
	}

	if a := sess.GetAddr(); a != nil {
		delete(m.addrMap, a.String())
	}

	if roomSessions, ok := m.roomMap[sess.RoomID]; ok {
		delete(roomSessions, sessionID)
		if len(roomSessions) == 0 {
			delete(m.roomMap, sess.RoomID)
		}
	}
}

func (m *Manager) CleanupInactive(timeout time.Duration) []uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var removed []uint32

	for sessionID, sess := range m.sessions {
		// session has its own lock for lastActivity
		last := sess.LastActivity()
		if now.Sub(last) <= timeout {
			continue
		}

		removed = append(removed, sessionID)

		delete(m.sessions, sessionID)
		delete(m.userRoom, userRoomKey(sess.UserID, sess.RoomID))

		delete(m.ssrcMap, sess.SSRC)
		if sess.VideoSSRC != 0 {
			delete(m.ssrcMap, sess.VideoSSRC)
		}

		if a := sess.GetAddr(); a != nil {
			delete(m.addrMap, a.String())
		}

		if roomSessions, ok := m.roomMap[sess.RoomID]; ok {
			delete(roomSessions, sessionID)
			if len(roomSessions) == 0 {
				delete(m.roomMap, sess.RoomID)
			}
		}
	}

	return removed
}

func (m *Manager) GetActiveSessions(activeWithin time.Duration) []*Session {
	cutoff := time.Now().Add(-activeWithin)

	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.LastActivity().After(cutoff) {
			out = append(out, s)
		}
	}
	return out
}
