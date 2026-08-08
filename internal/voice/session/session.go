package session

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
)

// Transport is a session's egress channel. UDP sessions leave it nil (the
// router uses the UDP socket); TCP-backed sessions set it so forwarded media and
// replies are length-prefixed onto the stream.
type Transport interface {
	WritePacket(data []byte) error
}

// Session is one participant's authoritative server-side state: identity, its
// three media SSRCs, current address/activity, crypto (current + retained previous
// for rotation), media flags, quality, capabilities, sequence/timestamp counters,
// and per-SSRC retransmit/sequence/subscription state. All mutable fields are
// guarded by Mu; the exported fields are read under it too. The zero value is not
// usable — construct via Manager.CreateSession.
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
	prevCrypto    *crypto.SessionCrypto
	Muted         bool
	VideoEnabled  bool
	ScreenSharing bool
	Speaking      bool
	IsObserver    bool
	inactive      bool
	transport     Transport

	QualityPrefs map[uint32]uint8

	LastQuality    int
	LastRTTMs      float64
	LastPacketLoss float64
	LastJitterMs   float64

	Caps sessionCaps

	Mu sync.RWMutex

	AudioSeq  uint16
	VideoSeq  uint16
	ScreenSeq uint16
	AudioTS   uint32
	VideoTS   uint32

	retransmitBufs map[uint32]*RetransmitBuffer
	SeqTrackers    map[uint32]*SeqTracker
	Subscriptions  map[uint32]bool
	subsInited     bool
}

// sessionCaps holds a session's negotiated Opus capabilities: forward error
// correction, discontinuous transmission, and the client's max bitrate (bps).
type sessionCaps struct {
	FEC        bool
	DTX        bool
	MaxBitrate uint32
}

// SeqTracker estimates packet loss on one SSRC from RTP sequence numbers. It
// accumulates the gaps between expected and received sequences (handling 16-bit
// wraparound) against the total seen. It is not internally synchronized; callers
// hold Session.Mu.
type SeqTracker struct {
	last   uint16
	inited bool
	gaps   uint32
	total  uint32
}

// Feed records sequence seq and returns the number of packets apparently skipped
// since the previous one (0 if in order or the first). Small backward/huge jumps
// (reorder or reset) are treated as no gap. The first call just seeds state.
func (t *SeqTracker) Feed(seq uint16) (gap int) {
	t.total++
	if !t.inited {
		t.inited = true
		t.last = seq
		return 0
	}
	expected := t.last + 1
	t.last = seq
	if seq == expected {
		return 0
	}
	diff := int(seq) - int(expected)
	if diff < 0 {
		diff += 65536
	}
	if diff > 0 && diff < 1000 {
		t.gaps += uint32(diff)
		return diff
	}
	return 0
}

// LossRate returns estimated loss as gaps/(received+gaps), in [0,1). It reports 0
// until at least 20 packets have been seen, to avoid noisy estimates on tiny
// samples.
func (t *SeqTracker) LossRate() float64 {
	if t.total < 20 {
		return 0
	}
	return float64(t.gaps) / float64(t.total+uint32(t.gaps))
}

// Reset clears the accumulated gap/total counters (but keeps last/inited), used
// after acting on a loss burst so the next estimate starts fresh.
func (t *SeqTracker) Reset() {
	t.gaps = 0
	t.total = 0
}

// cloneUDPAddr deep-copies addr (including its IP slice) so a stored address can't
// be mutated by the caller. Returns nil for a nil input.
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

// udpAddrEqual reports whether two UDP addresses are the same IP/port/zone,
// treating two nils as equal. Used to detect address changes for migration.
func udpAddrEqual(a, b *net.UDPAddr) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	return a.Port == b.Port && a.Zone == b.Zone && a.IP.Equal(b.IP)
}

// udpAddrKey returns the addrMap lookup key for addr ("" for nil).
func udpAddrKey(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

// GetAddr returns a defensive copy of the session's current address (nil if
// unbound), safe to read concurrently.
func (s *Session) GetAddr() *net.UDPAddr {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return cloneUDPAddr(s.addr)
}

// LastActivity returns the time of the last Touch, read under the lock. Used by
// the sweeper to decide inactivity/removal.
func (s *Session) LastActivity() time.Time {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return s.lastActivity
}

// touchLocked stamps lastActivity; the caller must already hold s.Mu.
func (s *Session) touchLocked(now time.Time) { s.lastActivity = now }

// AddrChanged reports whether addr differs from the session's bound address. Used
// to reject control packets whose source doesn't match the session (anti-spoof).
func (s *Session) AddrChanged(addr *net.UDPAddr) bool {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return !udpAddrEqual(s.addr, addr)
}

// replaceAddr atomically swaps the session's stored address to a copy of addr,
// reporting the old and new addrMap keys and whether anything changed. Returns
// changed=false (and empty keys) when the address is unchanged, so the Manager can
// skip re-indexing.
func (s *Session) replaceAddr(addr *net.UDPAddr) (oldKey, newKey string, changed bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	if udpAddrEqual(s.addr, addr) {
		return "", "", false
	}
	oldKey = udpAddrKey(s.addr)
	s.addr = cloneUDPAddr(addr)
	newKey = udpAddrKey(s.addr)
	return oldKey, newKey, true
}

// SetMuted sets the audio-muted flag under the lock. A muted sender's audio is
// dropped at routing time.
func (s *Session) SetMuted(muted bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.Muted = muted
}

// SetVideoEnabled sets the camera-on flag under the lock.
func (s *Session) SetVideoEnabled(enabled bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.VideoEnabled = enabled
}

// SetScreenSharing sets the screen-share flag under the lock.
func (s *Session) SetScreenSharing(enabled bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.ScreenSharing = enabled
}

// SetSpeaking sets the speaking flag under the lock.
func (s *Session) SetSpeaking(speaking bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.Speaking = speaking
}

// IsInactive reports whether the session has been marked inactive by the sweeper
// (idle past the inactive threshold but not yet removed).
func (s *Session) IsInactive() bool {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return s.inactive
}

// markInactive flags the session inactive; returns true only on the transition
// (so the caller announces ParticipantLeft exactly once).
func (s *Session) markInactive() bool {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if s.inactive {
		return false
	}
	s.inactive = true
	return true
}

// SetTransport installs (or clears, with nil) the session's egress transport. A
// non-nil transport makes the router send over TCP instead of the UDP socket.
func (s *Session) SetTransport(t Transport) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.transport = t
}

// Transport returns the session's egress transport, or nil for a UDP session.
func (s *Session) Transport() Transport {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return s.transport
}

// SetCrypto installs the current session cipher, retaining the previous one (a
// different KeyID) so in-flight media under the old key is still accepted during
// a rotation overlap.
func (s *Session) SetCrypto(sc *crypto.SessionCrypto) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if sc != nil && s.Crypto != nil && s.Crypto.KeyID != sc.KeyID {
		s.prevCrypto = s.Crypto
	}
	s.Crypto = sc
}

// CryptoForKeyID returns the cipher matching the wire key id (current, or the
// retained previous during a rotation overlap), falling back to current.
func (s *Session) CryptoForKeyID(keyID uint8) *crypto.SessionCrypto {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	if s.Crypto != nil && s.Crypto.KeyID == keyID {
		return s.Crypto
	}
	if s.prevCrypto != nil && s.prevCrypto.KeyID == keyID {
		return s.prevCrypto
	}
	return s.Crypto
}

// SetQuality records the client's latest self-reported quality/RTT/loss/jitter
// under the lock, for inclusion in roster snapshots.
func (s *Session) SetQuality(quality int, rttMs, packetLoss, jitterMs float64) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.LastQuality = quality
	s.LastRTTMs = rttMs
	s.LastPacketLoss = packetLoss
	s.LastJitterMs = jitterMs
}

// SnapshotQuality returns the last recorded quality, rttMs, packetLoss, jitterMs
// as an atomic read under the lock.
func (s *Session) SnapshotQuality() (int, float64, float64, float64) {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return s.LastQuality, s.LastRTTMs, s.LastPacketLoss, s.LastJitterMs
}

// SetCapabilities replaces the session's Opus capabilities under the lock.
func (s *Session) SetCapabilities(fec, dtx bool, maxBitrate uint32) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.Caps = sessionCaps{FEC: fec, DTX: dtx, MaxBitrate: maxBitrate}
}

// GetCapabilities returns the session's FEC, DTX, and max bitrate (bps).
func (s *Session) GetCapabilities() (bool, bool, uint32) {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return s.Caps.FEC, s.Caps.DTX, s.Caps.MaxBitrate
}

// NextAudioSeq returns the current audio sequence number and post-increments it
// (wrapping at 2^16) under the lock. Used by server-originated audio.
func (s *Session) NextAudioSeq() uint16 {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	seq := s.AudioSeq
	s.AudioSeq++
	return seq
}

// NextVideoSeq returns the current video sequence number and post-increments it
// (wrapping at 2^16) under the lock.
func (s *Session) NextVideoSeq() uint16 {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	seq := s.VideoSeq
	s.VideoSeq++
	return seq
}

// StoreForRetransmit copies a video packet into the per-SSRC retransmit buffer
// (lazily creating a 750ms buffer) so it can answer later NACKs. The buffer copies
// the data, so data may be reused after this returns. Concurrency-safe.
func (s *Session) StoreForRetransmit(ssrc uint32, seq uint16, timestamp uint32, keyframe bool, data []byte) {
	s.Mu.RLock()
	buf := s.retransmitBufs[ssrc]
	s.Mu.RUnlock()
	if buf == nil {
		s.Mu.Lock()
		if s.retransmitBufs[ssrc] == nil {
			s.retransmitBufs[ssrc] = NewRetransmitBuffer(750 * time.Millisecond)
		}
		buf = s.retransmitBufs[ssrc]
		s.Mu.Unlock()
	}
	buf.Store(seq, timestamp, keyframe, data)
}

// GetForRetransmit returns the cached bytes for (ssrc, seq) if still buffered and
// unexpired, else nil. The returned slice aliases buffer storage; treat read-only.
func (s *Session) GetForRetransmit(ssrc uint32, seq uint16) []byte {
	s.Mu.RLock()
	buf := s.retransmitBufs[ssrc]
	s.Mu.RUnlock()
	if buf == nil {
		return nil
	}
	return buf.Get(seq)
}

func (s *Session) UpdateSubscriptions(subs []uint32) {
	// An empty list is ignored. It is almost always a premature/stale update the
	// client sends before it knows the real participant set; applying it would
	// flip the session from the un-inited default ("receive all") to "receive
	// nothing", blacking out both audio and video until a later control packet —
	// which may be lost over UDP — arrives to correct it. A genuine subscription
	// always names at least one SSRC, so this only drops spurious resets.
	if len(subs) == 0 {
		return
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.subsInited = true
	s.Subscriptions = make(map[uint32]bool, len(subs))
	for _, ssrc := range subs {
		s.Subscriptions[ssrc] = true
	}
}

// maxPinnedPackets caps how many packets of the current keyframe may be pinned
// (kept past maxAge) so a large keyframe can still be fully retransmitted without
// letting the buffer grow without bound.
const maxPinnedPackets = 256

// RetransmitBuffer caches recently sent packets of one SSRC (keyed by sequence)
// for NACK-driven retransmission. Ordinary packets expire after maxAge; packets of
// the latest keyframe are "pinned" (up to maxPinnedPackets) so a joiner can always
// recover the current keyframe. Concurrency-safe via mu.
type RetransmitBuffer struct {
	mu         sync.RWMutex
	packets    map[uint16]*CachedPacket
	maxAge     time.Duration
	pinnedTS   uint32
	havePinned bool
	pinnedN    int
}

// CachedPacket is one buffered packet: its owned Data copy, store time, sequence,
// and whether it is pinned to the current keyframe (exempt from age expiry).
type CachedPacket struct {
	Data      []byte
	Timestamp time.Time
	Sequence  uint16
	pinned    bool
}

// NewRetransmitBuffer returns an empty buffer whose non-pinned packets expire
// after maxAge.
func NewRetransmitBuffer(maxAge time.Duration) *RetransmitBuffer {
	return &RetransmitBuffer{
		packets: make(map[uint16]*CachedPacket),
		maxAge:  maxAge,
	}
}

// Store caches a copy of data under seq, using the current time. keyframe marks
// the packet as part of a keyframe for pinning.
func (rb *RetransmitBuffer) Store(seq uint16, timestamp uint32, keyframe bool, data []byte) {
	rb.storeAt(seq, timestamp, keyframe, data, time.Now())
}

// storeAt is Store with an injectable clock (for tests). It copies data, and when
// keyframe is set for a new timestamp it unpins the previous keyframe and starts
// pinning the new one (up to maxPinnedPackets). It also evicts non-pinned packets
// older than maxAge on every store. Holds rb.mu.
func (rb *RetransmitBuffer) storeAt(seq uint16, timestamp uint32, keyframe bool, data []byte, now time.Time) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	pkt := &CachedPacket{Data: dataCopy, Timestamp: now, Sequence: seq}

	if keyframe {
		if !rb.havePinned || timestamp != rb.pinnedTS {
			for _, p := range rb.packets {
				p.pinned = false
			}
			rb.pinnedTS = timestamp
			rb.havePinned = true
			rb.pinnedN = 0
		}
		if rb.pinnedN < maxPinnedPackets {
			pkt.pinned = true
			rb.pinnedN++
		}
	}

	rb.packets[seq] = pkt
	for s, p := range rb.packets {
		if !p.pinned && now.Sub(p.Timestamp) > rb.maxAge {
			delete(rb.packets, s)
		}
	}
}

// Get returns the cached bytes for seq if present and still valid, using the
// current time.
func (rb *RetransmitBuffer) Get(seq uint16) []byte {
	return rb.getAt(seq, time.Now())
}

// getAt is Get with an injectable clock. It returns the packet's Data if it is
// pinned or not yet older than maxAge, else nil. The returned slice aliases the
// cached copy; do not mutate.
func (rb *RetransmitBuffer) getAt(seq uint16, now time.Time) []byte {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	if p, ok := rb.packets[seq]; ok && (p.pinned || now.Sub(p.Timestamp) <= rb.maxAge) {
		return p.Data
	}
	return nil
}

// roomEntry holds a room's sessions plus an immutable slice snapshot. The map is
// mutated only under Manager.mu; the snapshot is rebuilt on each change and stored
// atomically so readers (GetRoomSessions) can range it lock-free.
type roomEntry struct {
	sessions map[uint32]*Session
	snapshot atomic.Value
}

// newRoomEntry returns an empty room with an initialized (nil-slice) snapshot.
func newRoomEntry() *roomEntry {
	r := &roomEntry{sessions: make(map[uint32]*Session)}
	r.snapshot.Store([]*Session(nil))
	return r
}

// rebuildSnapshotLocked recomputes the atomic slice snapshot from the map. The
// caller must hold Manager.mu (the map's guard).
func (r *roomEntry) rebuildSnapshotLocked() {
	snap := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		snap = append(snap, s)
	}
	r.snapshot.Store(snap)
}

// Snapshot returns the current room-membership slice without locking. The slice is
// shared and immutable; callers must not modify it.
func (r *roomEntry) Snapshot() []*Session {
	v := r.snapshot.Load()
	if v == nil {
		return nil
	}
	return v.([]*Session)
}

// Manager is the authoritative session registry. It indexes sessions by ID, by
// user-in-room, by room, by SSRC, and by address, all guarded by mu, and hands out
// monotonically increasing session IDs and SSRCs. Per-session state lives behind
// each Session's own Mu.
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

// NewManager returns an empty Manager. Session IDs start at 1000 and SSRCs at 2000
// (leaving low values free for well-known/reserved use).
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

// userRoomKey builds the userRoom index key from a user and room ID.
func userRoomKey(userID, roomID string) string { return userID + ":" + roomID }

// CreateSession allocates a session and inserts it into every index under mu. A
// non-observer gets three consecutive SSRCs (audio, video, screen); an observer
// gets none (it only receives). The stored address is a copy. Returns the new
// session.
func (m *Manager) CreateSession(userID, roomID string, addr *net.UDPAddr, sessionCrypto *crypto.SessionCrypto, videoEnabled bool, observer bool) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	sessionID := m.nextID
	m.nextID++

	var audioSSRC, videoSSRC, screenSSRC uint32
	if !observer {
		audioSSRC = m.nextSSRC
		m.nextSSRC++
		videoSSRC = m.nextSSRC
		m.nextSSRC++
		screenSSRC = m.nextSSRC
		m.nextSSRC++
	}

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
		IsObserver:     observer,
		retransmitBufs: make(map[uint32]*RetransmitBuffer),
		SeqTrackers:    make(map[uint32]*SeqTracker),
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

	if audioSSRC > 0 {
		m.ssrcMap[audioSSRC] = sess
		m.ssrcMap[videoSSRC] = sess
		m.ssrcMap[screenSSRC] = sess
	}
	if sess.addr != nil {
		m.addrMap[udpAddrKey(sess.addr)] = sess
	}
	return sess
}

// BindAddr moves a session's address to addr and updates the addrMap index
// accordingly (removing the old key, adding the new). No-op if addr is nil, the
// session is gone, or the address is unchanged.
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

// Touch refreshes activity and clears the inactive flag. It returns true if the
// session was inactive (i.e. this call reactivated it) so the caller can
// re-announce the participant to the room.
func (m *Manager) Touch(sessionID uint32) bool {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return false
	}
	now := time.Now()
	sess.Mu.Lock()
	sess.touchLocked(now)
	reactivated := sess.inactive
	sess.inactive = false
	sess.Mu.Unlock()
	return reactivated
}

// GetSession returns the session with the given ID, or nil if absent.
func (m *Manager) GetSession(sessionID uint32) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

// GetBySSRC returns the session owning ssrc (audio, video, or screen), or nil.
func (m *Manager) GetBySSRC(ssrc uint32) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ssrcMap[ssrc]
}

// GetByAddr returns the session currently bound to addr, or nil for an unknown or
// nil address. This is the media fast-path lookup.
func (m *Manager) GetByAddr(addr *net.UDPAddr) *Session {
	if addr == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.addrMap[udpAddrKey(addr)]
}

// GetSessionByUserInRoom returns the user's session in the given room, or nil.
// Used during Hello to detect reconnects and observer/active collisions.
func (m *Manager) GetSessionByUserInRoom(userID, roomID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.userRoom[userRoomKey(userID, roomID)]
}

// GetRoomSessions returns the room's membership snapshot (nil for an empty/unknown
// room). The result is a shared immutable slice safe to range without locking.
func (m *Manager) GetRoomSessions(roomID string) []*Session {
	m.mu.RLock()
	room := m.roomMap[roomID]
	m.mu.RUnlock()
	if room == nil {
		return nil
	}
	return room.Snapshot()
}

// RemoveSession deletes a session from every index and drops the room entirely
// once its last member leaves (otherwise it rebuilds the room snapshot). No-op if
// the session is already gone.
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

// InactiveInfo describes a session that just transitioned to inactive during a
// sweep, carrying the identity and SSRCs needed to broadcast its ParticipantLeft.
type InactiveInfo struct {
	SessionID  uint32
	RoomID     string
	UserID     string
	SSRC       uint32
	VideoSSRC  uint32
	ScreenSSRC uint32
}

// SweepInactive is the two-stage failure lifecycle: a session idle past
// inactiveAfter is marked inactive (returned once in nowInactive so peers can be
// told it left); a session idle past removeAfter is fully removed. Touch clears
// the inactive flag, enabling SSRC/crypto-preserving resume within the window.
func (m *Manager) SweepInactive(inactiveAfter, removeAfter time.Duration, now time.Time) (nowInactive []InactiveInfo, removed []uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	touchedRooms := make(map[string]*roomEntry)
	for sessionID, sess := range m.sessions {
		idle := now.Sub(sess.LastActivity())
		if idle > removeAfter {
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
			continue
		}
		if idle > inactiveAfter && sess.markInactive() {
			nowInactive = append(nowInactive, InactiveInfo{
				SessionID:  sessionID,
				RoomID:     sess.RoomID,
				UserID:     sess.UserID,
				SSRC:       sess.SSRC,
				VideoSSRC:  sess.VideoSSRC,
				ScreenSSRC: sess.ScreenSSRC,
			})
		}
	}
	for _, room := range touchedRooms {
		room.rebuildSnapshotLocked()
	}
	return nowInactive, removed
}

// GetActiveSessions returns every session whose last activity is within
// activeWithin of now. Order is unspecified (map iteration).
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

// SetQualityPref records this receiver's desired simulcast tier for source ssrc
// (lazily creating the map), consulted by the router for layer selection.
func (s *Session) SetQualityPref(ssrc uint32, tier uint8) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if s.QualityPrefs == nil {
		s.QualityPrefs = make(map[uint32]uint8)
	}
	s.QualityPrefs[ssrc] = tier
}

// GetQualityPref returns the receiver's preferred tier for ssrc and whether one was
// set. When none is set it returns (LayerLarge, false); the false signals the
// router that layer selection is opt-out for this stream (forward everything).
func (s *Session) GetQualityPref(ssrc uint32) (uint8, bool) {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	if s.QualityPrefs == nil {
		return 3, false
	}
	tier, ok := s.QualityPrefs[ssrc]
	return tier, ok
}

// IsSubscribedTo reports whether this receiver wants ssrc forwarded. Before the
// client has sent any subscription (subsInited false) the default is "receive all"
// for non-observers and "nothing" for observers; afterward it is the explicit set.
func (s *Session) IsSubscribedTo(ssrc uint32) bool {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	if !s.subsInited {
		return !s.IsObserver
	}
	return s.Subscriptions[ssrc]
}
