package status

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/version"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

// Participant is one member's live state in a voice room, as exposed by the
// status API. SSRC is the audio stream; VideoSSRC/ScreenSSRC are zero (and
// omitted) when that stream is inactive. Quality/RTTMs/PacketLoss/JitterMs come
// from the session's latest quality snapshot and are omitted when zero.
type Participant struct {
	UserID        string  `json:"user_id"`
	SSRC          uint32  `json:"ssrc"`
	VideoSSRC     uint32  `json:"video_ssrc,omitempty"`
	ScreenSSRC    uint32  `json:"screen_ssrc,omitempty"`
	Muted         bool    `json:"muted"`
	VideoEnabled  bool    `json:"video_enabled"`
	ScreenSharing bool    `json:"screen_sharing"`
	Speaking      bool    `json:"speaking"`
	Quality       int     `json:"quality,omitempty"`
	RTTMs         float64 `json:"rtt_ms,omitempty"`
	PacketLoss    float64 `json:"packet_loss,omitempty"`
	JitterMs      float64 `json:"jitter_ms,omitempty"`
	JoinedAt      string  `json:"joined_at"`
}

// RoomInfo describes a room and its participants. In the room-list view
// Participants is left nil and only Count is populated; the detail view fills both.
type RoomInfo struct {
	RoomID       string        `json:"room_id"`
	Participants []Participant `json:"participants"`
	Count        int           `json:"count"`
}

// ServerInfo is the /stats payload: uptime, aggregate room/session counts, a
// per-room occupancy map, and the optional telemetry Stats snapshot.
type ServerInfo struct {
	Version        string           `json:"version"`
	Uptime         string           `json:"uptime"`
	ActiveRooms    int              `json:"active_rooms"`
	ActiveSessions int              `json:"active_sessions"`
	Rooms          map[string]int   `json:"rooms"`
	Metrics        *telemetry.Stats `json:"metrics,omitempty"`
}

// Server is the read-only HTTP status API for operators/dashboards, serving
// room and stats views over the live session manager. All data endpoints are
// JWT-gated via auth; /health is open.
type Server struct {
	sessions  *session.Manager
	jwt       *jwt.Manager
	metrics   *telemetry.Metrics
	logger    *zap.Logger
	startTime time.Time
}

// NewServer wires the status API to the session manager, JWT validator, and
// (optionally nil) metrics, starting its uptime clock now.
func NewServer(sm *session.Manager, jm *jwt.Manager, m *telemetry.Metrics, l *zap.Logger) *Server {
	return &Server{sessions: sm, jwt: jm, metrics: m, logger: l, startTime: time.Now()}
}

// Start serves the status API (CORS-wrapped, 10s read/write timeouts) on port,
// blocking until ctx is cancelled — then it drains with a 5s Shutdown timeout —
// or ListenAndServe fails. Returns nil on clean context-cancelled shutdown.
func (s *Server) Start(ctx context.Context, port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/voice/rooms", s.auth(s.listRooms))
	mux.HandleFunc("/v1/voice/rooms/", s.auth(s.roomDetail))
	mux.HandleFunc("/v1/voice/stats", s.auth(s.stats))
	mux.HandleFunc("/v1/voice/health", s.health)

	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: cors(mux), ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	s.logger.Info("status API starting", zap.Int("port", port))

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// cors wraps next with permissive CORS (any origin, GET/OPTIONS) and short-circuits
// preflight OPTIONS with 204, so browser dashboards can call the API cross-origin.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth wraps a handler to require a valid Bearer access token, responding 401
// when the Authorization header is missing or the token fails validation.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if h == "" {
			writeErr(w, http.StatusUnauthorized, "missing authorization header")
			return
		}
		token := strings.TrimPrefix(h, "Bearer ")
		if _, err := s.jwt.ValidateAccessToken(token); err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next(w, r)
	}
}

// listRooms returns every active room with only its participant count (no
// per-participant detail); use roomDetail for members.
func (s *Server) listRooms(w http.ResponseWriter, r *http.Request) {
	rooms := s.sessions.GetAllRooms()
	result := make([]RoomInfo, 0, len(rooms))
	for _, rid := range rooms {
		sess := s.sessions.GetRoomSessions(rid)
		result = append(result, RoomInfo{RoomID: rid, Count: len(sess)})
	}
	writeJSON(w, result)
}

// roomDetail returns full participant state for the room id in the trailing path
// segment, responding 400 when the id is empty. Returns an empty participant
// list (not 404) for an unknown room.
func (s *Server) roomDetail(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/voice/rooms/"), "/")
	if roomID == "" {
		writeErr(w, http.StatusBadRequest, "room_id required")
		return
	}
	sess := s.sessions.GetRoomSessions(roomID)
	ps := make([]Participant, 0, len(sess))
	for _, se := range sess {
		quality, rttMs, packetLoss, jitterMs := se.SnapshotQuality()
		ps = append(ps, Participant{
			UserID:        se.UserID,
			SSRC:          se.SSRC,
			VideoSSRC:     se.VideoSSRC,
			ScreenSSRC:    se.ScreenSSRC,
			Muted:         se.Muted,
			VideoEnabled:  se.VideoEnabled,
			ScreenSharing: se.ScreenSharing,
			Speaking:      se.Speaking,
			Quality:       quality,
			RTTMs:         rttMs,
			PacketLoss:    packetLoss,
			JitterMs:      jitterMs,
			JoinedAt:      se.JoinedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, RoomInfo{RoomID: roomID, Participants: ps, Count: len(ps)})
}

// stats returns server-wide occupancy (room count, total sessions, per-room
// counts) plus the telemetry Stats snapshot when metrics is configured.
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	rooms := s.sessions.GetAllRooms()
	rc := make(map[string]int, len(rooms))
	total := 0
	for _, rid := range rooms {
		n := len(s.sessions.GetRoomSessions(rid))
		rc[rid] = n
		total += n
	}
	info := ServerInfo{Version: version.Voice(), Uptime: time.Since(s.startTime).Truncate(time.Second).String(), ActiveRooms: len(rooms), ActiveSessions: total, Rooms: rc}
	if s.metrics != nil {
		st := s.metrics.GetStats()
		info.Metrics = &st
	}
	writeJSON(w, info)
}

// health is the unauthenticated liveness endpoint; it always reports ok with the
// build version and performs no dependency checks.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"status": "ok", "version": version.Voice()})
}

// writeJSON sets the JSON content type and encodes v; encoding errors are ignored
// since the status may already be committed.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr writes a {"error": msg} JSON body with the given HTTP status code.
func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
