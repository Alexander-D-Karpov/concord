package voiceassign

import (
	"context"
	"crypto/rand"
	stderrors "errors"
	"fmt"
	"hash/fnv"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	apperr "github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
)

const (
	// voiceServerTTL caps how long a best-server pick is cached per region; short
	// so load/placement changes are picked up quickly.
	voiceServerTTL = 30 * time.Second
	// cryptoTTL is the lifetime of a room's cached crypto suite (long — a call's
	// key is stable until membership changes rotate it).
	cryptoTTL = 24 * time.Hour
	// voiceTokenTTL is how long an issued voice JWT is valid; clients reconnect
	// with a fresh assignment after it lapses.
	voiceTokenTTL = 5 * time.Minute
	// serverStaleAfter is the heartbeat grace period: a server whose updated_at is
	// older than this is treated as offline and never handed out.
	serverStaleAfter = 90 * time.Second
)

// RoomServerAssigner persists a room's pinned voice server. The rooms repository
// satisfies it; used to re-pin a room when its server dies.
type RoomServerAssigner interface {
	AssignVoiceServer(ctx context.Context, roomID, serverID uuid.UUID) error
}

// EventPublisher lets voiceassign notify affected users of placement changes
// (e.g. a voice server going offline). *events.Hub satisfies it.
type EventPublisher interface {
	BroadcastToUser(userID string, event *streamv1.ServerEvent)
}

// Service places voice/DM-call participants onto voice servers: it selects a
// server by region/load, pins rooms, issues short-lived voice tokens, manages
// per-room crypto suites, and tracks in-memory sessions. It also runs a health
// loop that evicts dead servers and re-homes affected calls. Safe for concurrent
// use; DB access goes through pool, hot lookups through cache.
type Service struct {
	pool       *pgxpool.Pool
	jwtManager *jwt.Manager
	cache      *cache.Cache
	rooms      RoomServerAssigner
	sessions   *sessionStore
	pub        EventPublisher
	tcpPort    int
}

// sessionStore is the in-memory index of active voice sessions and per-room
// placement (crypto suite, assigned UDP port, server id), all guarded by mu.
// portCount bounds the deterministic per-room port fan-out from a server's base
// port.
type sessionStore struct {
	mu         sync.RWMutex
	byRoom     map[string]map[string]*VoiceSession
	byUser     map[string]*VoiceSession
	roomCrypto map[string]CryptoSuite
	roomPort   map[string]int
	roomServer map[string]string
	portCount  int
}

// VoiceSession is one user's membership in a room's voice call, with media
// preference flags. JoinedAt is a Unix timestamp (seconds).
type VoiceSession struct {
	UserID        string
	RoomID        string
	ServerID      string
	Muted         bool
	VideoEnabled  bool
	ScreenSharing bool
	JoinedAt      int64
}

// VoiceAssignmentResult is what a client needs to join: the chosen server, its
// UDP endpoint and optional TCP fallback endpoint, a voice token, the token
// lifetime in seconds, codec hints, and the room's crypto suite.
type VoiceAssignmentResult struct {
	ServerID    string
	Endpoint    UDPEndpoint
	TCPEndpoint UDPEndpoint
	VoiceToken  string
	ExpiresIn   int
	Codec       CodecHint
	Crypto      CryptoSuite
}

// UDPEndpoint is a host:port media endpoint. A zero-value endpoint (empty host,
// zero port) signals "not available" — e.g. TCP fallback disabled.
type UDPEndpoint struct {
	Host string
	Port int
}

// CodecHint tells the client which audio/video codecs to use. Video is empty for
// audio-only calls.
type CodecHint struct {
	Audio string
	Video string
}

// CryptoSuite is a room's media encryption material: AEAD name, 4-byte KeyID,
// 32-byte KeyMaterial, and 12-byte NonceBase. Rotated on membership change.
type CryptoSuite struct {
	AEAD        string
	KeyID       []byte
	KeyMaterial []byte
	NonceBase   []byte
}

// VoiceParticipant is the externally reported view of a room member. Speaking is
// not tracked server-side here and is always false.
type VoiceParticipant struct {
	UserID        string
	Muted         bool
	VideoEnabled  bool
	ScreenSharing bool
	Speaking      bool
	JoinedAt      int64
}

// voiceServer is a resolved server row: its id, region, and the UDP host/port
// parsed from addr_udp.
type voiceServer struct {
	ID     uuid.UUID
	Host   string
	Port   int
	Region string
}

// offlineServer identifies a server that just transitioned to offline, carried
// from the DB update to the eviction/notification step.
type offlineServer struct {
	ID     string
	Region string
}

// NewService builds a Service. cacheClient, roomsRepo, and pub may be nil, in
// which case caching, room re-pinning, and user notifications are skipped
// respectively. The session store defaults to a 50-port fan-out per server.
func NewService(pool *pgxpool.Pool, jwtManager *jwt.Manager, cacheClient *cache.Cache, roomsRepo RoomServerAssigner, pub EventPublisher) *Service {
	return &Service{
		pool:       pool,
		jwtManager: jwtManager,
		cache:      cacheClient,
		rooms:      roomsRepo,
		pub:        pub,
		sessions: &sessionStore{
			byRoom:     make(map[string]map[string]*VoiceSession),
			byUser:     make(map[string]*VoiceSession),
			roomCrypto: make(map[string]CryptoSuite),
			roomPort:   make(map[string]int),
			roomServer: make(map[string]string),
			portCount:  50,
		},
	}
}

// errNoVoiceServer is Unavailable (retryable), not Internal: the request is
// fine, the fleet is empty.
func errNoVoiceServer() error {
	return apperr.NewAppError(codes.Unavailable, "no available voice server", nil)
}

// SetPortCount sets how many distinct UDP ports rooms are hashed across above a
// server's base port. Changing it does not move rooms already assigned a port.
func (s *Service) SetPortCount(count int) {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	s.sessions.portCount = count
}

// SetTCPPort sets the voice servers' TCP/TLS fallback port (0 = disabled); the
// endpoint host reuses the server's UDP host.
func (s *Service) SetTCPPort(port int) { s.tcpPort = port }

// tcpEndpointFor returns the TCP fallback endpoint (host:tcpPort), or a zero
// endpoint when the fallback is disabled (tcpPort <= 0).
func (s *Service) tcpEndpointFor(host string) UDPEndpoint {
	if s.tcpPort <= 0 {
		return UDPEndpoint{}
	}
	return UDPEndpoint{Host: host, Port: s.tcpPort}
}

// assignPort returns the room's UDP port, reusing the prior assignment when the
// room is still on serverID, otherwise deterministically hashing roomID into
// [basePort, basePort+portCount) and recording it. Caller must hold ss.mu.
func (ss *sessionStore) assignPort(roomID, serverID string, basePort int) int {
	if existingServer, ok := ss.roomServer[roomID]; ok && existingServer == serverID {
		if port, ok := ss.roomPort[roomID]; ok {
			return port
		}
	}

	h := fnv.New32a()
	_, _ = h.Write([]byte(roomID))
	port := basePort + int(h.Sum32()%uint32(ss.portCount))
	ss.roomPort[roomID] = port
	ss.roomServer[roomID] = serverID
	return port
}

// voiceServerCacheKey is the cache key for the best-server pick in a region;
// an empty region maps to "default".
func (s *Service) voiceServerCacheKey(region string) string {
	if region == "" {
		region = "default"
	}
	return fmt.Sprintf("voice:server:%s", region)
}

// cryptoCacheKey is the cache key for a room's crypto suite.
func (s *Service) cryptoCacheKey(roomID string) string {
	return "voice:crypto:" + roomID
}

// AssignToVoice places userID into a channel room's voice call. It honors the
// room's pinned voice server when that server is still alive, otherwise picks a
// fresh one by region/load and re-pins the room so later joiners agree. Returns
// codes.Unavailable when no server is available.
func (s *Service) AssignToVoice(ctx context.Context, roomID, userID, region string, audioOnly bool) (*VoiceAssignmentResult, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, apperr.BadRequest("invalid room_id")
	}

	var roomVoiceServerID *uuid.UUID
	err = s.pool.QueryRow(ctx, `
		SELECT voice_server_id FROM rooms WHERE id = $1 AND deleted_at IS NULL
	`, roomUUID).Scan(&roomVoiceServerID)
	if err != nil && !stderrors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Internal("failed to get room", err)
	}

	var server *voiceServer
	if roomVoiceServerID != nil {
		server, err = s.getServerByID(ctx, *roomVoiceServerID)
		if err != nil {
			// pinned server is offline or stale; pick a fresh one and persist it
			// so subsequent joiners read the correct placement
			server, err = s.getBestServer(ctx, region)
			if err != nil {
				return nil, err
			}
			s.repinRoom(ctx, roomID, roomUUID, server.ID)
		}
	} else {
		server, err = s.getBestServer(ctx, region)
		if err != nil {
			return nil, err
		}
	}

	return s.createAssignment(ctx, roomID, userID, server, audioOnly)
}

// repinRoom persists a new voice server for a room whose pinned server went
// offline, and clears the room's crypto/port so the next assignment regenerates
// them against the new server. Existing in-memory sessions are left untouched —
// migration applies to new joiners only.
func (s *Service) repinRoom(ctx context.Context, roomID string, roomUUID, newServerID uuid.UUID) {
	if s.rooms != nil {
		if err := s.rooms.AssignVoiceServer(ctx, roomUUID, newServerID); err != nil {
			log.Printf("[VoiceAssign] failed to repin room %s to server %s: %v", roomID, newServerID, err)
		}
	}
	s.clearRoomPlacement(ctx, roomID)
}

// clearRoomPlacement forgets a room's cached crypto, port, and server binding
// (in memory and in the cache) so the next assignment regenerates them.
func (s *Service) clearRoomPlacement(ctx context.Context, roomID string) {
	s.sessions.mu.Lock()
	delete(s.sessions.roomCrypto, roomID)
	delete(s.sessions.roomPort, roomID)
	delete(s.sessions.roomServer, roomID)
	s.sessions.mu.Unlock()

	if s.cache != nil {
		_ = s.cache.Delete(ctx, s.cryptoCacheKey(roomID))
	}
}

// CheckAndReassign marks heartbeat-lapsed servers offline and, for each one that
// just transitioned, invalidates its region's server cache and evicts its
// sessions (notifying users to rejoin). Returns the DB error if the sweep fails.
func (s *Service) CheckAndReassign(ctx context.Context) error {
	stale, err := s.markStaleServersOffline(ctx)
	if err != nil {
		return err
	}

	for _, srv := range stale {
		log.Printf("[VoiceAssign] voice server %s (%s) missed heartbeats; marking offline", srv.ID, srv.Region)
		s.invalidateServerCache(ctx, srv.Region)
		s.clearSessionsForServer(ctx, srv.ID)
	}

	return nil
}

// markStaleServersOffline flips servers whose heartbeat lapsed to 'offline' and
// returns only the ones that transitioned, so placement never hands out a dead
// server and the transition is announced exactly once.
func (s *Service) markStaleServersOffline(ctx context.Context) ([]offlineServer, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE voice_servers
		SET status = 'offline', updated_at = updated_at
		WHERE status = 'online' AND updated_at < NOW() - $1::interval
		RETURNING id, COALESCE(region, '')
	`, serverStaleAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stale []offlineServer
	for rows.Next() {
		var id uuid.UUID
		var region string
		if err := rows.Scan(&id, &region); err != nil {
			return nil, err
		}
		stale = append(stale, offlineServer{ID: id.String(), Region: region})
	}
	return stale, rows.Err()
}

// invalidateServerCache drops the cached best-server pick for region and for the
// default bucket, so a dead server is not re-handed-out from cache. No-op when
// caching is disabled.
func (s *Service) invalidateServerCache(ctx context.Context, region string) {
	if s.cache == nil {
		return
	}
	_ = s.cache.Delete(ctx, s.voiceServerCacheKey(region), s.voiceServerCacheKey(""))
}

// clearSessionsForServer drops dead sessions and clears placement for affected
// rooms so the next joiner gets reassigned to a healthy server. It does not
// rewrite existing sessions or re-issue tokens (migration is new-joiners-only).
func (s *Service) clearSessionsForServer(ctx context.Context, offlineServerID string) {
	type affectedUser struct{ userID, roomID string }
	var affected []affectedUser

	s.sessions.mu.Lock()
	affectedRooms := make(map[string]bool)

	for roomID, sessions := range s.sessions.byRoom {
		for userID, session := range sessions {
			if session.ServerID == offlineServerID {
				delete(sessions, userID)
				delete(s.sessions.byUser, userID)
				affectedRooms[roomID] = true
				affected = append(affected, affectedUser{userID: userID, roomID: roomID})
			}
		}
		if len(sessions) == 0 {
			delete(s.sessions.byRoom, roomID)
		}
	}

	for roomID := range affectedRooms {
		delete(s.sessions.roomCrypto, roomID)
		delete(s.sessions.roomPort, roomID)
		delete(s.sessions.roomServer, roomID)
	}
	s.sessions.mu.Unlock()

	if s.cache != nil {
		for roomID := range affectedRooms {
			_ = s.cache.Delete(ctx, s.cryptoCacheKey(roomID))
		}
	}

	// Notify affected users so their clients auto-rejoin on a healthy server
	// instead of stranding on the dead one.
	if s.pub != nil {
		for _, a := range affected {
			s.pub.BroadcastToUser(a.userID, &streamv1.ServerEvent{
				Payload: &streamv1.ServerEvent_VoiceServerChanged{
					VoiceServerChanged: &streamv1.VoiceServerChanged{
						RoomId: a.roomID,
						UserId: a.userID,
						Reason: "server_offline",
					},
				},
			})
		}
	}
}

// StartHealthChecker runs CheckAndReassign every interval until ctx is
// cancelled. Blocks, so run it in its own goroutine; check errors are logged and
// do not stop the loop.
func (s *Service) StartHealthChecker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.CheckAndReassign(ctx); err != nil {
				log.Printf("[VoiceAssign] health check failed: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// AssignToDMCall places userID into a DM channel's call with no server
// preference, selecting purely by region/load. See AssignToDMCallOnServer.
func (s *Service) AssignToDMCall(ctx context.Context, channelID, userID, region string, audioOnly bool) (*VoiceAssignmentResult, error) {
	return s.AssignToDMCallOnServer(ctx, channelID, userID, "", region, audioOnly)
}

// AssignToDMCallOnServer keeps an in-progress call on preferredServerID when
// that server is still alive, and otherwise places it by region/load.
// preferredServerID and region are distinct: passing a server id as the region
// matches no rows and strands the call.
func (s *Service) AssignToDMCallOnServer(ctx context.Context, channelID, userID, preferredServerID, region string, audioOnly bool) (*VoiceAssignmentResult, error) {
	channelUUID, err := uuid.Parse(channelID)
	if err != nil {
		return nil, apperr.BadRequest("invalid channel_id")
	}

	var exists bool
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM dm_channels WHERE id = $1)
	`, channelUUID).Scan(&exists)
	if err != nil {
		return nil, apperr.Internal("failed to verify dm channel", err)
	}
	if !exists {
		return nil, apperr.NotFound("dm channel not found")
	}

	var server *voiceServer
	if preferredServerID != "" {
		if id, perr := uuid.Parse(preferredServerID); perr == nil {
			if srv, serr := s.getServerByID(ctx, id); serr == nil {
				server = srv
			} else {
				log.Printf("[VoiceAssign] dm call %s: pinned server %s unavailable (%v); reassigning",
					channelID, preferredServerID, serr)
			}
		}
	}

	if server == nil {
		server, err = s.getBestServer(ctx, region)
		if err != nil {
			return nil, err
		}
	}

	return s.createAssignment(ctx, channelID, userID, server, audioOnly)
}

// getServerByID loads an online, non-stale server by id, parsing its UDP
// host/port. Returns errNoVoiceServer (Unavailable) when the server is missing,
// offline, or stale — signalling the caller to reassign.
func (s *Service) getServerByID(ctx context.Context, serverID uuid.UUID) (*voiceServer, error) {
	var server voiceServer
	var addrUDP string

	err := s.pool.QueryRow(ctx, `
		SELECT id, addr_udp, COALESCE(region, '')
		FROM voice_servers
		WHERE id = $1 AND status = 'online' AND updated_at > NOW() - $2::interval
	`, serverID, serverStaleAfter).Scan(&server.ID, &addrUDP, &server.Region)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, errNoVoiceServer()
	}
	if err != nil {
		return nil, apperr.Internal("failed to load voice server", err)
	}

	server.Host, server.Port = parseUDPAddr(addrUDP)
	return &server, nil
}

// getBestServer returns the least-loaded live server for region, checking the
// cache first and caching the pick on a DB hit. If the region has no live
// server it falls back to any region; only an empty fleet yields
// errNoVoiceServer.
func (s *Service) getBestServer(ctx context.Context, region string) (*voiceServer, error) {
	cacheKey := s.voiceServerCacheKey(region)

	if s.cache != nil {
		var cached voiceServer
		if err := s.cache.Get(ctx, cacheKey, &cached); err == nil && cached.ID != uuid.Nil {
			return &cached, nil
		}
	}

	server, err := s.queryBestServer(ctx, region)
	if err != nil {
		return nil, err
	}

	// A region with no live server is a config mismatch, not a reason to fail
	// the call: fall back to any online server.
	if server == nil && region != "" {
		server, err = s.queryBestServer(ctx, "")
		if err != nil {
			return nil, err
		}
		if server != nil {
			log.Printf("[VoiceAssign] no online server in region %q; falling back to %s (region %q)",
				region, server.ID, server.Region)
		}
	}

	if server == nil {
		return nil, errNoVoiceServer()
	}

	if s.cache != nil {
		_ = s.cache.Set(ctx, cacheKey, server, voiceServerTTL)
	}

	return server, nil
}

// queryBestServer returns the least-loaded server whose heartbeat is fresh, or
// (nil, nil) when none match. Staleness is checked here as well as in the health
// checker so a server that dies between ticks is never handed out.
func (s *Service) queryBestServer(ctx context.Context, region string) (*voiceServer, error) {
	query := `
		SELECT id, addr_udp, COALESCE(region, '')
		FROM voice_servers
		WHERE status = 'online' AND updated_at > NOW() - $1::interval
	`
	args := []interface{}{serverStaleAfter}

	if region != "" {
		query += " AND region = $2"
		args = append(args, region)
	}

	query += " ORDER BY load_score ASC LIMIT 1"

	var server voiceServer
	var addrUDP string
	err := s.pool.QueryRow(ctx, query, args...).Scan(&server.ID, &addrUDP, &server.Region)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Internal("failed to find voice server", err)
	}

	server.Host, server.Port = parseUDPAddr(addrUDP)
	return &server, nil
}

// createAssignment builds the join result for a resolved server: it issues a
// voice token, resolves/creates the room crypto suite, assigns the room's UDP
// port, records the in-memory session, and sets codec hints (video omitted when
// audioOnly). It is the shared tail of the AssignTo* entry points.
func (s *Service) createAssignment(ctx context.Context, roomOrChannelID, userID string, server *voiceServer, audioOnly bool) (*VoiceAssignmentResult, error) {
	voiceToken, err := s.jwtManager.GenerateVoiceToken(userID, roomOrChannelID, server.ID.String(), voiceTokenTTL)
	if err != nil {
		return nil, apperr.Internal("failed to generate voice token", err)
	}

	cryptoSuite, err := s.getOrCreateRoomCrypto(ctx, roomOrChannelID)
	if err != nil {
		return nil, err
	}

	s.sessions.mu.Lock()
	assignedPort := s.sessions.assignPort(roomOrChannelID, server.ID.String(), server.Port)
	s.sessions.mu.Unlock()

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

	codec := CodecHint{Audio: "opus", Video: "h264"}
	if audioOnly {
		codec.Video = ""
	}

	return &VoiceAssignmentResult{
		ServerID: server.ID.String(),
		Endpoint: UDPEndpoint{
			Host: server.Host,
			Port: assignedPort,
		},
		TCPEndpoint: s.tcpEndpointFor(server.Host),
		VoiceToken:  voiceToken,
		ExpiresIn:   int(voiceTokenTTL / time.Second),
		Codec:       codec,
		Crypto:      cryptoSuite,
	}, nil
}

// LeaveVoice removes userID's session from the room's in-memory store. Always
// returns nil (the signature keeps a uniform error contract for callers).
func (s *Service) LeaveVoice(ctx context.Context, roomID, userID string) error {
	s.sessions.remove(roomID, userID)
	return nil
}

// RotateSharedRooms rotates the voice key of every active-call room that
// contains both users (used when one blocks the other).
func (s *Service) RotateSharedRooms(ctx context.Context, userA, userB string) error {
	s.sessions.mu.RLock()
	var rooms []string
	for roomID, members := range s.sessions.byRoom {
		if _, okA := members[userA]; !okA {
			continue
		}
		if _, okB := members[userB]; okB {
			rooms = append(rooms, roomID)
		}
	}
	s.sessions.mu.RUnlock()
	for _, roomID := range rooms {
		if err := s.RotateRoomKey(ctx, roomID); err != nil {
			return err
		}
	}
	return nil
}

// RotateRoomKey generates a fresh room crypto suite (incrementing the wire KeyID
// byte), replaces the stored/cached suite so new media uses it, and notifies
// current members so their clients adopt it. Called on membership change so a
// departed user can no longer decrypt future media.
func (s *Service) RotateRoomKey(ctx context.Context, roomID string) error {
	s.sessions.mu.RLock()
	_, hasCrypto := s.sessions.roomCrypto[roomID]
	_, hasRoom := s.sessions.byRoom[roomID]
	s.sessions.mu.RUnlock()
	if !hasCrypto && !hasRoom {
		return nil // no established voice call in this room; nothing to rotate
	}

	newCS, err := generateCryptoSuite()
	if err != nil {
		return err
	}

	s.sessions.mu.Lock()
	if old, ok := s.sessions.roomCrypto[roomID]; ok && len(old.KeyID) > 0 && len(newCS.KeyID) > 0 {
		newCS.KeyID[0] = old.KeyID[0] + 1
	}
	s.sessions.roomCrypto[roomID] = newCS
	var members []string
	if room, ok := s.sessions.byRoom[roomID]; ok {
		members = make([]string, 0, len(room))
		for userID := range room {
			members = append(members, userID)
		}
	}
	s.sessions.mu.Unlock()

	if s.cache != nil {
		_ = s.cache.Set(ctx, s.cryptoCacheKey(roomID), newCS, cryptoTTL)
	}

	if s.pub != nil {
		for _, userID := range members {
			s.pub.BroadcastToUser(userID, &streamv1.ServerEvent{
				Payload: &streamv1.ServerEvent_VoiceKeyRotated{
					VoiceKeyRotated: &streamv1.VoiceKeyRotated{
						RoomId:      roomID,
						KeyId:       newCS.KeyID,
						KeyMaterial: newCS.KeyMaterial,
						NonceBase:   newCS.NonceBase,
						Aead:        newCS.AEAD,
					},
				},
			})
		}
	}
	return nil
}

// UpdateMediaPrefs sets a member's muted/video/screen-share flags on their live
// session. Returns NotFound when the user has no session in the room.
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
	return apperr.NotFound("session not found")
}

// GetVoiceParticipants returns the room's current members as VoiceParticipants
// (Speaking always false; not tracked here). Never errors — an unknown room
// yields an empty slice.
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

// getOrCreateRoomCrypto returns the room's crypto suite, checking cache then the
// in-memory store and only generating a new suite on a miss. It uses cache SetNX
// to resolve races so concurrent first-joiners across instances converge on one
// suite. Falls back to local state on cache errors.
func (s *Service) getOrCreateRoomCrypto(ctx context.Context, roomID string) (CryptoSuite, error) {
	cacheKey := s.cryptoCacheKey(roomID)

	if s.cache != nil {
		var existing CryptoSuite
		err := s.cache.Get(ctx, cacheKey, &existing)
		if err == nil && validCrypto(existing) {
			return existing, nil
		}
		if err != nil && !stderrors.Is(err, cache.ErrCacheMiss) {
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

// validCrypto reports whether cs has a non-empty AEAD and correctly sized fields
// (4-byte KeyID, 32-byte key, 12-byte nonce base), guarding against partial or
// corrupt cached entries.
func validCrypto(cs CryptoSuite) bool {
	return cs.AEAD != "" && len(cs.KeyID) == 4 && len(cs.KeyMaterial) == 32 && len(cs.NonceBase) == 12
}

// generateCryptoSuite creates a fresh aes256-gcm suite with cryptographically
// random KeyID, KeyMaterial, and NonceBase. Returns an error if the system RNG
// fails.
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

// add stores session, first evicting the user's prior session from its old room
// (and clearing that room's placement if it becomes empty) so a user is only
// ever in one voice room. Concurrency-safe.
func (ss *sessionStore) add(session *VoiceSession) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if existing, exists := ss.byUser[session.UserID]; exists {
		if room, ok := ss.byRoom[existing.RoomID]; ok {
			delete(room, existing.UserID)
			if len(room) == 0 {
				delete(ss.byRoom, existing.RoomID)
				delete(ss.roomCrypto, existing.RoomID)
				delete(ss.roomPort, existing.RoomID)
				delete(ss.roomServer, existing.RoomID)
			}
		}
	}

	if _, exists := ss.byRoom[session.RoomID]; !exists {
		ss.byRoom[session.RoomID] = make(map[string]*VoiceSession)
	}
	ss.byRoom[session.RoomID][session.UserID] = session
	ss.byUser[session.UserID] = session
}

// remove deletes userID's session from roomID, dropping the room's crypto/port/
// server placement when the room empties. No-op if the user or room is absent.
func (ss *sessionStore) remove(roomID, userID string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if room, exists := ss.byRoom[roomID]; exists {
		delete(room, userID)
		if len(room) == 0 {
			delete(ss.byRoom, roomID)
			delete(ss.roomCrypto, roomID)
			delete(ss.roomPort, roomID)
			delete(ss.roomServer, roomID)
		}
	}
	delete(ss.byUser, userID)
}

// getRoomParticipants returns a snapshot slice of the room's sessions, or nil if
// the room is unknown. The slice is fresh; the *VoiceSession elements are shared.
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

// parseUDPAddr splits a stored address into host and port, tolerating optional
// udp:// or tcp:// prefixes. It falls back to port 50000 when the port is
// missing or out of the 1..65535 range.
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

// InvalidateVoiceCache drops the room's cached crypto suite. No-op (nil) when
// caching is disabled.
func (s *Service) InvalidateVoiceCache(ctx context.Context, roomID string) error {
	if s.cache == nil {
		return nil
	}
	_ = s.cache.Delete(ctx, s.cryptoCacheKey(roomID))
	return nil
}
