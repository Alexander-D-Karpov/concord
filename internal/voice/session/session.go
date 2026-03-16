package session

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
)

type Session struct {
	ID         uint32
	UserID     string
	RoomID     string
	SSRC       uint32
	VideoSSRC  uint32
	ScreenSSRC uint32
	JoinedAt   time.Time

	addr         *net.UDPAddr
	lastActivity time.Time

	Crypto        *crypto.SessionCrypto
	Muted         bool
	VideoEnabled  bool
	ScreenSharing bool
	Speaking      bool

	Subscriptions map[uint32]bool

	LastQuality    int
	LastRTTMs      float64
	LastPacketLoss float64
	LastJitterMs   float64

	mu sync.RWMutex

	AudioSeq  uint16
	VideoSeq  uint16
	ScreenSeq uint16
	AudioTS   uint32
	VideoTS   uint32

	retransmitBufs map[uint32]*RetransmitBuffer
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	cp := *addr
	if addr.IP != nil {
		cp.IP = append(net.IP(nil), addr.IP...)
	}
	return &cp
}

func udpAddrEqual(a, b *net.UDPAddr) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	return a.Port == b.Port && a.Zone == b.Zone && a.IP.Equal(b.IP)
}

func udpAddrKey(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

func (s *Session) GetAddr() *net.UDPAddr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneUDPAddr(s.addr)
}

func (s *Session) LastActivity() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastActivity
}

func (s *Session) touchLocked(now time.Time) { s.lastActivity = now }

func (s *Session) AddrChanged(addr *net.UDPAddr) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !udpAddrEqual(s.addr, addr)
}

func (s *Session) replaceAddr(addr *net.UDPAddr) (oldKey, newKey string, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if udpAddrEqual(s.addr, addr) {
		return "", "", false
	}
	oldKey = udpAddrKey(s.addr)
	s.addr = cloneUDPAddr(addr)
	newKey = udpAddrKey(s.addr)
	return oldKey, newKey, true
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

func (s *Session) SetScreenSharing(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ScreenSharing = enabled
}

func (s *Session) SetSpeaking(speaking bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Speaking = speaking
}

func (s *Session) SetQuality(quality int, rttMs, packetLoss, jitterMs float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastQuality = quality
	s.LastRTTMs = rttMs
	s.LastPacketLoss = packetLoss
	s.LastJitterMs = jitterMs
}

func (s *Session) SnapshotQuality() (int, float64, float64, float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.LastQuality, s.LastRTTMs, s.LastPacketLoss, s.LastJitterMs
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

func (s *Session) StoreForRetransmit(ssrc uint32, seq uint16, data []byte) {
	s.mu.RLock()
	buf := s.retransmitBufs[ssrc]
	s.mu.RUnlock()
	if buf == nil {
		s.mu.Lock()
		if s.retransmitBufs[ssrc] == nil {
			s.retransmitBufs[ssrc] = NewRetransmitBuffer(750 * time.Millisecond)
		}
		buf = s.retransmitBufs[ssrc]
		s.mu.Unlock()
	}
	buf.Store(seq, data)
}

func (s *Session) GetForRetransmit(ssrc uint32, seq uint16) []byte {
	s.mu.RLock()
	buf := s.retransmitBufs[ssrc]
	s.mu.RUnlock()
	if buf == nil {
		return nil
	}
	return buf.Get(seq)
}

func (s *Session) UpdateSubscriptions(subs []uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(subs) == 0 {
		s.Subscriptions = nil
		return
	}
	s.Subscriptions = make(map[uint32]bool, len(subs))
	for _, ssrc := range subs {
		s.Subscriptions[ssrc] = true
	}
}

func (s *Session) IsSubscribedTo(ssrc uint32) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Subscriptions == nil {
		return true
	}
	return s.Subscriptions[ssrc]
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
	rb.packets[seq] = &CachedPacket{Data: dataCopy, Timestamp: now, Sequence: seq}
	for s, p := range rb.packets {
		if now.Sub(p.Timestamp) > rb.maxAge {
			delete(rb.packets, s)
		}
	}
}

func (rb *RetransmitBuffer) Get(seq uint16) []byte {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	if p, ok := rb.packets[seq]; ok && time.Since(p.Timestamp) <= rb.maxAge {
		return p.Data
	}
	return nil
}

type roomEntry struct {
	sessions map[uint32]*Session
	snapshot atomic.Value
}

func newRoomEntry() *roomEntry {
	r := &roomEntry{sessions: make(map[uint32]*Session)}
	r.snapshot.Store([]*Session(nil))
	return r
}

func (r *roomEntry) rebuildSnapshotLocked() {
	snap := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		snap = append(snap, s)
	}
	r.snapshot.Store(snap)
}

func (r *roomEntry) Snapshot() []*Session {
	v := r.snapshot.Load()
	if v == nil {
		return nil
	}
	return v.([]*Session)
}

type Manager struct {
	mu sync.RWMutex

	sessions map[uint32]*Session
	userRoom map[string]*Session
	roomMap  map[string]*roomEntry
	ssrcMap  map[uint32]*Session
	addrMap  map[string]*Session

	nextID   uint32
	nextSSRC uint32
}

func NewManager() *Manager {
	return &Manager{
		sessions: make(map[uint32]*Session),
		userRoom: make(map[string]*Session),
		roomMap:  make(map[string]*roomEntry),
		ssrcMap:  make(map[uint32]*Session),
		addrMap:  make(map[string]*Session),
		nextID:   1000,
		nextSSRC: 2000,
	}
}

func userRoomKey(userID, roomID string) string { return userID + ":" + roomID }

func (m *Manager) CreateSession(userID, roomID string, addr *net.UDPAddr, sessionCrypto *crypto.SessionCrypto, videoEnabled bool) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	sessionID := m.nextID
	m.nextID++
	audioSSRC := m.nextSSRC
	m.nextSSRC++
	videoSSRC := m.nextSSRC
	m.nextSSRC++
	screenSSRC := m.nextSSRC
	m.nextSSRC++

	sess := &Session{
		ID:             sessionID,
		UserID:         userID,
		RoomID:         roomID,
		SSRC:           audioSSRC,
		VideoSSRC:      videoSSRC,
		ScreenSSRC:     screenSSRC,
		JoinedAt:       now,
		addr:           cloneUDPAddr(addr),
		lastActivity:   now,
		Crypto:         sessionCrypto,
		VideoEnabled:   videoEnabled,
		retransmitBufs: make(map[uint32]*RetransmitBuffer),
	}

	m.sessions[sessionID] = sess
	m.userRoom[userRoomKey(userID, roomID)] = sess

	room := m.roomMap[roomID]
	if room == nil {
		room = newRoomEntry()
		m.roomMap[roomID] = room
	}
	room.sessions[sessionID] = sess
	room.rebuildSnapshotLocked()

	m.ssrcMap[audioSSRC] = sess
	m.ssrcMap[videoSSRC] = sess
	m.ssrcMap[screenSSRC] = sess
	if sess.addr != nil {
		m.addrMap[udpAddrKey(sess.addr)] = sess
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
	oldKey, newKey, changed := sess.replaceAddr(addr)
	if !changed {
		return
	}
	if oldKey != "" {
		delete(m.addrMap, oldKey)
	}
	if newKey != "" {
		m.addrMap[newKey] = sess
	}
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
	return m.addrMap[udpAddrKey(addr)]
}

func (m *Manager) GetSessionByUserInRoom(userID, roomID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.userRoom[userRoomKey(userID, roomID)]
}

func (m *Manager) GetRoomSessions(roomID string) []*Session {
	m.mu.RLock()
	room := m.roomMap[roomID]
	m.mu.RUnlock()
	if room == nil {
		return nil
	}
	return room.Snapshot()
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
	delete(m.ssrcMap, sess.VideoSSRC)
	delete(m.ssrcMap, sess.ScreenSSRC)
	if sess.addr != nil {
		delete(m.addrMap, udpAddrKey(sess.addr))
	}
	if room := m.roomMap[sess.RoomID]; room != nil {
		delete(room.sessions, sessionID)
		if len(room.sessions) == 0 {
			delete(m.roomMap, sess.RoomID)
		} else {
			room.rebuildSnapshotLocked()
		}
	}
}

func (m *Manager) CleanupInactive(timeout time.Duration) []uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var removed []uint32
	touchedRooms := make(map[string]*roomEntry)
	for sessionID, sess := range m.sessions {
		last := sess.LastActivity()
		if now.Sub(last) <= timeout {
			continue
		}
		removed = append(removed, sessionID)
		delete(m.sessions, sessionID)
		delete(m.userRoom, userRoomKey(sess.UserID, sess.RoomID))
		delete(m.ssrcMap, sess.SSRC)
		delete(m.ssrcMap, sess.VideoSSRC)
		delete(m.ssrcMap, sess.ScreenSSRC)
		if sess.addr != nil {
			delete(m.addrMap, udpAddrKey(sess.addr))
		}
		if room := m.roomMap[sess.RoomID]; room != nil {
			delete(room.sessions, sessionID)
			if len(room.sessions) == 0 {
				delete(m.roomMap, sess.RoomID)
			} else {
				touchedRooms[sess.RoomID] = room
			}
		}
	}
	for _, room := range touchedRooms {
		room.rebuildSnapshotLocked()
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
