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

const (
	voiceServerTTL = 1 * time.Minute
	cryptoTTL      = 24 * time.Hour
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
	UserID        string
	RoomID        string
	ServerID      string
	Muted         bool
	VideoEnabled  bool
	ScreenSharing bool
	JoinedAt      int64
}

type VoiceAssignmentResult struct {
	ServerID   string
	Endpoint   UDPEndpoint
	VoiceToken string
	Codec      CodecHint
	Crypto     CryptoSuite
}

type UDPEndpoint struct {
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
	UserID        string
	Muted         bool
	VideoEnabled  bool
	ScreenSharing bool
	Speaking      bool
	JoinedAt      int64
}

type voiceServer struct {
	ID     uuid.UUID
	Host   string
	Port   int
	Region string
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

func (s *Service) voiceServerCacheKey(region string) string {
	if region == "" {
		region = "default"
	}
	return fmt.Sprintf("voice:server:%s", region)
}

func (s *Service) cryptoCacheKey(roomID string) string {
	return "voice:crypto:" + roomID
}

func (s *Service) AssignToVoice(ctx context.Context, roomID, userID string, audioOnly bool) (*VoiceAssignmentResult, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, fmt.Errorf("invalid room_id: %w", err)
	}

	var roomVoiceServerID *uuid.UUID
	err = s.pool.QueryRow(ctx, `
		SELECT voice_server_id FROM rooms WHERE id = $1 AND deleted_at IS NULL
	`, roomUUID).Scan(&roomVoiceServerID)
	if err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("failed to get room: %w", err)
	}

	var server *voiceServer
	if roomVoiceServerID != nil {
		server, err = s.getServerByID(ctx, *roomVoiceServerID)
		if err != nil {
			server, err = s.getBestServer(ctx, "")
			if err != nil {
				return nil, err
			}
			s.migrateRoomParticipants(ctx, roomID, server.ID.String())
		}
	} else {
		server, err = s.getBestServer(ctx, "")
		if err != nil {
			return nil, err
		}
	}

	return s.createAssignment(ctx, roomID, userID, server, audioOnly)
}

func (s *Service) migrateRoomParticipants(ctx context.Context, roomID, newServerID string) {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()

	room, exists := s.sessions.byRoom[roomID]
	if !exists {
		return
	}

	for _, session := range room {
		session.ServerID = newServerID
	}

	delete(s.sessions.roomCrypto, roomID)
}

func (s *Service) CheckAndReassign(ctx context.Context) error {
	offlineServers, err := s.getOfflineServers(ctx)
	if err != nil {
		return err
	}

	for _, serverID := range offlineServers {
		s.reassignFromServer(ctx, serverID)
	}

	return nil
}

func (s *Service) getOfflineServers(ctx context.Context) ([]string, error) {
	query := `
		SELECT id FROM voice_servers 
		WHERE status != 'online' OR updated_at < NOW() - INTERVAL '2 minutes'
	`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id.String())
	}

	return ids, rows.Err()
}

func (s *Service) reassignFromServer(ctx context.Context, offlineServerID string) {
	s.sessions.mu.Lock()
	affectedRooms := make(map[string][]*VoiceSession)

	for roomID, sessions := range s.sessions.byRoom {
		for _, session := range sessions {
			if session.ServerID == offlineServerID {
				affectedRooms[roomID] = append(affectedRooms[roomID], session)
			}
		}
	}
	s.sessions.mu.Unlock()

	for roomID, sessions := range affectedRooms {
		newServer, err := s.getBestServer(ctx, "")
		if err != nil {
			continue
		}

		s.sessions.mu.Lock()
		for _, session := range sessions {
			session.ServerID = newServer.ID.String()
		}
		delete(s.sessions.roomCrypto, roomID)
		s.sessions.mu.Unlock()
	}
}

func (s *Service) StartHealthChecker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_ = s.CheckAndReassign(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) AssignToDMCall(ctx context.Context, channelID, userID string, audioOnly bool) (*VoiceAssignmentResult, error) {
	channelUUID, err := uuid.Parse(channelID)
	if err != nil {
		return nil, fmt.Errorf("invalid channel_id: %w", err)
	}

	var exists bool
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM dm_channels WHERE id = $1)
	`, channelUUID).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("failed to verify dm channel: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("dm channel not found")
	}

	server, err := s.getBestServer(ctx, "")
	if err != nil {
		return nil, err
	}

	return s.createAssignment(ctx, channelID, userID, server, audioOnly)
}

func (s *Service) getServerByID(ctx context.Context, serverID uuid.UUID) (*voiceServer, error) {
	var server voiceServer
	var addrUDP string

	err := s.pool.QueryRow(ctx, `
		SELECT id, addr_udp, COALESCE(region, '') FROM voice_servers 
		WHERE id = $1 AND status = 'online'
	`, serverID).Scan(&server.ID, &addrUDP, &server.Region)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("voice server offline or not found")
	}
	if err != nil {
		return nil, err
	}

	server.Host, server.Port = parseUDPAddr(addrUDP)
	return &server, nil
}

func (s *Service) getBestServer(ctx context.Context, region string) (*voiceServer, error) {
	cacheKey := s.voiceServerCacheKey(region)

	if s.cache != nil {
		var cached voiceServer
		if err := s.cache.Get(ctx, cacheKey, &cached); err == nil && cached.ID != uuid.Nil {
			return &cached, nil
		}
	}

	query := `
		SELECT id, addr_udp, COALESCE(region, '') FROM voice_servers
		WHERE status = 'online'
	`
	args := []interface{}{}

	if region != "" {
		query += " AND region = $1"
		args = append(args, region)
	}

	query += " ORDER BY load_score ASC LIMIT 1"

	var server voiceServer
	var addrUDP string
	err := s.pool.QueryRow(ctx, query, args...).Scan(&server.ID, &addrUDP, &server.Region)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("no available voice server")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find voice server: %w", err)
	}

	server.Host, server.Port = parseUDPAddr(addrUDP)

	if s.cache != nil {
		_ = s.cache.Set(ctx, cacheKey, server, voiceServerTTL)
	}

	return &server, nil
}

func (s *Service) createAssignment(ctx context.Context, roomOrChannelID, userID string, server *voiceServer, audioOnly bool) (*VoiceAssignmentResult, error) {
	voiceToken, err := s.jwtManager.GenerateVoiceToken(userID, roomOrChannelID, server.ID.String(), 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed to generate voice token: %w", err)
	}

	cryptoSuite, err := s.getOrCreateRoomCrypto(ctx, roomOrChannelID)
	if err != nil {
		return nil, err
	}

	session := &VoiceSession{
		UserID:        userID,
		RoomID:        roomOrChannelID,
		ServerID:      server.ID.String(),
		Muted:         false,
		VideoEnabled:  !audioOnly,
		ScreenSharing: false,
		JoinedAt:      time.Now().Unix(),
	}
	s.sessions.add(session)

	return &VoiceAssignmentResult{
		ServerID: server.ID.String(),
		Endpoint: UDPEndpoint{
			Host: server.Host,
			Port: server.Port,
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

func (s *Service) UpdateMediaPrefs(ctx context.Context, roomID, userID string, muted, videoEnabled, screenSharing bool) error {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()

	if room, exists := s.sessions.byRoom[roomID]; exists {
		if session, ok := room[userID]; ok {
			session.Muted = muted
			session.VideoEnabled = videoEnabled
			session.ScreenSharing = screenSharing
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
			UserID:        sess.UserID,
			Muted:         sess.Muted,
			VideoEnabled:  sess.VideoEnabled,
			ScreenSharing: sess.ScreenSharing,
			Speaking:      false,
			JoinedAt:      sess.JoinedAt,
		}
	}
	return participants, nil
}

func (s *Service) getOrCreateRoomCrypto(ctx context.Context, roomID string) (CryptoSuite, error) {
	cacheKey := s.cryptoCacheKey(roomID)

	if s.cache != nil {
		var existing CryptoSuite
		err := s.cache.Get(ctx, cacheKey, &existing)
		if err == nil && validCrypto(existing) {
			return existing, nil
		}
		if err != nil && !errors.Is(err, cache.ErrCacheMiss) {
			log.Printf("[VoiceAssign] Cache error: %v, falling back to local", err)
		}
	}

	s.sessions.mu.Lock()
	if cs, ok := s.sessions.roomCrypto[roomID]; ok && validCrypto(cs) {
		s.sessions.mu.Unlock()
		return cs, nil
	}
	s.sessions.mu.Unlock()

	newCS, err := generateCryptoSuite()
	if err != nil {
		return CryptoSuite{}, err
	}

	if s.cache != nil {
		ok, err := s.cache.SetNX(ctx, cacheKey, newCS, cryptoTTL)
		if err == nil && !ok {
			var existing CryptoSuite
			if err := s.cache.Get(ctx, cacheKey, &existing); err == nil && validCrypto(existing) {
				return existing, nil
			}
		}
	}

	s.sessions.mu.Lock()
	s.sessions.roomCrypto[roomID] = newCS
	s.sessions.mu.Unlock()

	return newCS, nil
}

func validCrypto(cs CryptoSuite) bool {
	return cs.AEAD != "" && len(cs.KeyID) == 4 && len(cs.KeyMaterial) == 32 && len(cs.NonceBase) == 12
}

func generateCryptoSuite() (CryptoSuite, error) {
	keyID := make([]byte, 4)
	keyMaterial := make([]byte, 32)
	nonceBase := make([]byte, 12)

	if _, err := rand.Read(keyID); err != nil {
		return CryptoSuite{}, fmt.Errorf("generate key_id: %w", err)
	}
	if _, err := rand.Read(keyMaterial); err != nil {
		return CryptoSuite{}, fmt.Errorf("generate key_material: %w", err)
	}
	if _, err := rand.Read(nonceBase); err != nil {
		return CryptoSuite{}, fmt.Errorf("generate nonce_base: %w", err)
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

func parseUDPAddr(addr string) (string, int) {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "udp://")
	addr = strings.TrimPrefix(addr, "tcp://")

	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		host := addr[:idx]
		portStr := addr[idx+1:]
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
			return host, port
		}
	}

	return addr, 50000
}

func (s *Service) InvalidateVoiceCache(ctx context.Context, roomID string) error {
	if s.cache == nil {
		return nil
	}
	_ = s.cache.Delete(ctx, s.cryptoCacheKey(roomID))
	return nil
}
