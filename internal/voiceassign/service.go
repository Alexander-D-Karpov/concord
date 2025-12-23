package voiceassign

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool       *pgxpool.Pool
	jwtManager *jwt.Manager
	cache      *cache.Cache
	sessions   *sessionStore
}

type sessionStore struct {
	mu         sync.RWMutex
	byRoom     map[string]map[string]*VoiceSession
	byUser     map[string]*VoiceSession
	roomCrypto map[string]CryptoSuite
}

type VoiceSession struct {
	UserID       string
	RoomID       string
	ServerID     string
	Muted        bool
	VideoEnabled bool
	JoinedAt     int64
}

type VoiceAssignmentResult struct {
	ServerID   string
	Endpoint   UdpEndpoint
	VoiceToken string
	Codec      CodecHint
	Crypto     CryptoSuite
}

type UdpEndpoint struct {
	Host string
	Port int
}

type CodecHint struct {
	Audio string
	Video string
}

type CryptoSuite struct {
	AEAD        string
	KeyID       []byte
	KeyMaterial []byte
	NonceBase   []byte
}

type VoiceParticipant struct {
	UserID       string
	Muted        bool
	VideoEnabled bool
	Speaking     bool
	JoinedAt     int64
}

func NewService(pool *pgxpool.Pool, jwtManager *jwt.Manager, cacheClient *cache.Cache) *Service {
	return &Service{
		pool:       pool,
		jwtManager: jwtManager,
		cache:      cacheClient,
		sessions: &sessionStore{
			byRoom:     make(map[string]map[string]*VoiceSession),
			byUser:     make(map[string]*VoiceSession),
			roomCrypto: make(map[string]CryptoSuite),
		},
	}
}

func (s *Service) AssignToVoice(ctx context.Context, roomID, userID string, audioOnly bool) (*VoiceAssignmentResult, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, fmt.Errorf("invalid room_id: %w", err)
	}

	var serverID uuid.UUID
	var addrUDP string

	var roomVoiceServerID *uuid.UUID
	err = s.pool.QueryRow(ctx, `
		SELECT voice_server_id FROM rooms WHERE id = $1 AND deleted_at IS NULL
	`, roomUUID).Scan(&roomVoiceServerID)

	if err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("failed to get room: %w", err)
	}

	if roomVoiceServerID != nil {
		err = s.pool.QueryRow(ctx, `
			SELECT id, addr_udp FROM voice_servers 
			WHERE id = $1 AND status = 'online'
		`, *roomVoiceServerID).Scan(&serverID, &addrUDP)

		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("configured voice server is offline")
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get voice server: %w", err)
		}
	} else {
		err = s.pool.QueryRow(ctx, `
			SELECT id, addr_udp FROM voice_servers
			WHERE status = 'online'
			ORDER BY load_score ASC
			LIMIT 1
		`).Scan(&serverID, &addrUDP)

		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("no available voice server")
		}
		if err != nil {
			return nil, fmt.Errorf("failed to find voice server: %w", err)
		}
	}

	host, port := parseUDPAddr(addrUDP)

	voiceToken, err := s.jwtManager.GenerateVoiceToken(userID, roomID, serverID.String(), 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed to generate voice token: %w", err)
	}

	cryptoSuite, err := s.getOrCreateRoomCrypto(ctx, roomID)
	if err != nil {
		return nil, err
	}

	session := &VoiceSession{
		UserID:       userID,
		RoomID:       roomID,
		ServerID:     serverID.String(),
		Muted:        false,
		VideoEnabled: !audioOnly,
		JoinedAt:     time.Now().Unix(),
	}
	s.sessions.add(session)

	return &VoiceAssignmentResult{
		ServerID: serverID.String(),
		Endpoint: UdpEndpoint{
			Host: host,
			Port: int(port),
		},
		VoiceToken: voiceToken,
		Codec: CodecHint{
			Audio: "opus",
			Video: "h264",
		},
		Crypto: cryptoSuite,
	}, nil
}

func (s *Service) LeaveVoice(ctx context.Context, roomID, userID string) error {
	s.sessions.remove(roomID, userID)
	return nil
}

func (s *Service) UpdateMediaPrefs(ctx context.Context, roomID, userID string, muted, videoEnabled bool) error {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()

	if room, exists := s.sessions.byRoom[roomID]; exists {
		if session, ok := room[userID]; ok {
			session.Muted = muted
			session.VideoEnabled = videoEnabled
			return nil
		}
	}
	return fmt.Errorf("session not found")
}

func (s *Service) GetVoiceParticipants(ctx context.Context, roomID string) ([]*VoiceParticipant, error) {
	sessions := s.sessions.getRoomParticipants(roomID)

	participants := make([]*VoiceParticipant, len(sessions))
	for i, sess := range sessions {
		participants[i] = &VoiceParticipant{
			UserID:       sess.UserID,
			Muted:        sess.Muted,
			VideoEnabled: sess.VideoEnabled,
			Speaking:     false,
			JoinedAt:     sess.JoinedAt,
		}
	}
	return participants, nil
}

func (s *Service) getOrCreateRoomCrypto(ctx context.Context, roomID string) (CryptoSuite, error) {
	const ttl = 24 * time.Hour
	cacheKey := "voice:crypto:" + roomID

	if s.cache == nil {
		log.Printf("[VoiceAssign] WARNING: Redis cache not available, using local storage")
		return s.getOrCreateRoomCryptoLocal(roomID)
	}

	var existing CryptoSuite
	err := s.cache.Get(ctx, cacheKey, &existing)
	if err == nil && validCrypto(existing) {
		log.Printf("[VoiceAssign] Using cached crypto for room %s", roomID)
		return existing, nil
	}

	if err != nil && !errors.Is(err, cache.ErrCacheMiss) {
		log.Printf("[VoiceAssign] Cache error: %v, falling back to local", err)
		return s.getOrCreateRoomCryptoLocal(roomID)
	}

	log.Printf("[VoiceAssign] Generating new crypto for room %s", roomID)
	newCS, err := generateCryptoSuite()
	if err != nil {
		return CryptoSuite{}, err
	}

	ok, err := s.cache.SetNX(ctx, cacheKey, newCS, ttl)
	if err == nil && ok {
		log.Printf("[VoiceAssign] Stored new crypto in cache for room %s", roomID)
		return newCS, nil
	}

	log.Printf("[VoiceAssign] SetNX failed or race, fetching existing for room %s", roomID)
	err = s.cache.Get(ctx, cacheKey, &existing)
	if err == nil && validCrypto(existing) {
		return existing, nil
	}

	return s.getOrCreateRoomCryptoLocal(roomID)
}

func (s *Service) getOrCreateRoomCryptoLocal(roomID string) (CryptoSuite, error) {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()

	if cs, ok := s.sessions.roomCrypto[roomID]; ok && validCrypto(cs) {
		return cs, nil
	}

	cs, err := generateCryptoSuite()
	if err != nil {
		return CryptoSuite{}, err
	}

	s.sessions.roomCrypto[roomID] = cs
	return cs, nil
}

func validCrypto(cs CryptoSuite) bool {
	return cs.AEAD != "" && len(cs.KeyID) == 4 && len(cs.KeyMaterial) == 32 && len(cs.NonceBase) == 12
}

func generateCryptoSuite() (CryptoSuite, error) {
	keyID := make([]byte, 4)
	keyMaterial := make([]byte, 32)
	nonceBase := make([]byte, 12)

	if _, err := rand.Read(keyID); err != nil {
		return CryptoSuite{}, fmt.Errorf("failed to generate key_id: %w", err)
	}
	if _, err := rand.Read(keyMaterial); err != nil {
		return CryptoSuite{}, fmt.Errorf("failed to generate key_material: %w", err)
	}
	if _, err := rand.Read(nonceBase); err != nil {
		return CryptoSuite{}, fmt.Errorf("failed to generate nonce_base: %w", err)
	}

	return CryptoSuite{
		AEAD:        "aes256-gcm",
		KeyID:       keyID,
		KeyMaterial: keyMaterial,
		NonceBase:   nonceBase,
	}, nil
}

func (ss *sessionStore) add(session *VoiceSession) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if existing, exists := ss.byUser[session.UserID]; exists {
		if room, ok := ss.byRoom[existing.RoomID]; ok {
			delete(room, existing.UserID)
			if len(room) == 0 {
				delete(ss.byRoom, existing.RoomID)
				delete(ss.roomCrypto, existing.RoomID)
			}
		}
	}

	if _, exists := ss.byRoom[session.RoomID]; !exists {
		ss.byRoom[session.RoomID] = make(map[string]*VoiceSession)
	}
	ss.byRoom[session.RoomID][session.UserID] = session
	ss.byUser[session.UserID] = session
}

func (ss *sessionStore) remove(roomID, userID string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if room, exists := ss.byRoom[roomID]; exists {
		delete(room, userID)
		if len(room) == 0 {
			delete(ss.byRoom, roomID)
			delete(ss.roomCrypto, roomID)
		}
	}
	delete(ss.byUser, userID)
}

func (ss *sessionStore) getRoomParticipants(roomID string) []*VoiceSession {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	room, exists := ss.byRoom[roomID]
	if !exists {
		return nil
	}

	sessions := make([]*VoiceSession, 0, len(room))
	for _, sess := range room {
		sessions = append(sessions, sess)
	}
	return sessions
}

func parseUDPAddr(addr string) (string, uint32) {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "udp://")
	addr = strings.TrimPrefix(addr, "tcp://")

	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		host := addr[:idx]
		portStr := addr[idx+1:]
		if port, err := strconv.ParseUint(portStr, 10, 32); err == nil && port > 0 && port <= 65535 {
			return host, uint32(port)
		}
	}

	return addr, 50000
}
