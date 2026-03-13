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

type Participant struct {
	UserID        string `json:"user_id"`
	SSRC          uint32 `json:"ssrc"`
	VideoSSRC     uint32 `json:"video_ssrc,omitempty"`
	ScreenSSRC    uint32 `json:"screen_ssrc,omitempty"`
	Muted         bool   `json:"muted"`
	VideoEnabled  bool   `json:"video_enabled"`
	ScreenSharing bool   `json:"screen_sharing"`
	Speaking      bool   `json:"speaking"`
	JoinedAt      string `json:"joined_at"`
}

type RoomInfo struct {
	RoomID       string        `json:"room_id"`
	Participants []Participant `json:"participants"`
	Count        int           `json:"count"`
}

type ServerInfo struct {
	Version        string           `json:"version"`
	Uptime         string           `json:"uptime"`
	ActiveRooms    int              `json:"active_rooms"`
	ActiveSessions int              `json:"active_sessions"`
	Rooms          map[string]int   `json:"rooms"`
	Metrics        *telemetry.Stats `json:"metrics,omitempty"`
}

type Server struct {
	sessions  *session.Manager
	jwt       *jwt.Manager
	metrics   *telemetry.Metrics
	logger    *zap.Logger
	startTime time.Time
}

func NewServer(sm *session.Manager, jm *jwt.Manager, m *telemetry.Metrics, l *zap.Logger) *Server {
	return &Server{sessions: sm, jwt: jm, metrics: m, logger: l, startTime: time.Now()}
}

func (s *Server) Start(ctx context.Context, port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/voice/rooms", s.auth(s.listRooms))
	mux.HandleFunc("/v1/voice/rooms/", s.auth(s.roomDetail))
	mux.HandleFunc("/v1/voice/stats", s.auth(s.stats))
	mux.HandleFunc("/v1/voice/health", s.health)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      cors(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
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

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if h == "" {
			writeErr(w, http.StatusUnauthorized, "missing authorization header")
			return
		}
		token := strings.TrimPrefix(h, "Bearer ")
		_, err := s.jwt.ValidateAccessToken(token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next(w, r)
	}
}

func (s *Server) listRooms(w http.ResponseWriter, r *http.Request) {
	rooms := s.sessions.GetAllRooms()
	result := make([]RoomInfo, 0, len(rooms))
	for _, rid := range rooms {
		sess := s.sessions.GetRoomSessions(rid)
		result = append(result, RoomInfo{RoomID: rid, Count: len(sess)})
	}
	writeJSON(w, result)
}

func (s *Server) roomDetail(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimPrefix(r.URL.Path, "/v1/voice/rooms/")
	roomID = strings.TrimSuffix(roomID, "/")
	if roomID == "" {
		writeErr(w, http.StatusBadRequest, "room_id required")
		return
	}
	sess := s.sessions.GetRoomSessions(roomID)
	ps := make([]Participant, 0, len(sess))
	for _, se := range sess {
		ps = append(ps, Participant{
			UserID:        se.UserID,
			SSRC:          se.SSRC,
			VideoSSRC:     se.VideoSSRC,
			ScreenSSRC:    se.ScreenSSRC,
			Muted:         se.Muted,
			VideoEnabled:  se.VideoEnabled,
			ScreenSharing: se.ScreenSharing,
			Speaking:      se.Speaking,
			JoinedAt:      se.LastActivity().Format(time.RFC3339),
		})
	}
	writeJSON(w, RoomInfo{RoomID: roomID, Participants: ps, Count: len(ps)})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	rooms := s.sessions.GetAllRooms()
	rc := make(map[string]int, len(rooms))
	total := 0
	for _, rid := range rooms {
		n := len(s.sessions.GetRoomSessions(rid))
		rc[rid] = n
		total += n
	}
	info := ServerInfo{
		Version:        version.Voice(),
		Uptime:         time.Since(s.startTime).Truncate(time.Second).String(),
		ActiveRooms:    len(rooms),
		ActiveSessions: total,
		Rooms:          rc,
	}
	if s.metrics != nil {
		st := s.metrics.GetStats()
		info.Metrics = &st
	}
	writeJSON(w, info)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"status": "ok", "version": version.Voice()})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
