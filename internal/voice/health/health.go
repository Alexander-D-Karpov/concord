package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/version"
	"go.uber.org/zap"
)

// Status is the health verdict serialized into JSON responses.
type Status string

const (
	// StatusHealthy means all checks passed.
	StatusHealthy Status = "healthy"
	// StatusDegraded is a defined intermediate state; note handleHealth never
	// emits it — the aggregate is only ever healthy or unhealthy today.
	StatusDegraded Status = "degraded"
	// StatusUnhealthy means at least one check returned an error; the endpoint
	// then responds 503.
	StatusUnhealthy Status = "unhealthy"
)

// Check is one named liveness probe's result. Error is omitted from JSON when empty.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Response is the aggregated health payload returned by the health endpoint.
// Status is the overall verdict; Uptime is a human-readable duration string.
type Response struct {
	Status    Status    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Checks    []Check   `json:"checks"`
	Uptime    string    `json:"uptime"`
	Version   string    `json:"version"`
}

// Server runs the HTTP health endpoint over a set of named check functions.
// The mutex guards the checks map so RegisterCheck can be called concurrently
// with request handling.
type Server struct {
	logger    *zap.Logger
	startTime time.Time
	checks    map[string]func(context.Context) error
	version   string
	mu        sync.RWMutex
}

// NewServer returns a health Server with no checks registered, its uptime clock
// started now, and the version fixed to the current voice build.
func NewServer(logger *zap.Logger) *Server {
	return &Server{
		logger:    logger,
		startTime: time.Now(),
		checks:    make(map[string]func(context.Context) error),
		version:   version.Voice(),
	}
}

// RegisterCheck adds or replaces the probe stored under name. A check reports
// health by returning nil and unhealth by returning an error. Safe to call
// concurrently with request handling.
func (s *Server) RegisterCheck(name string, check func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[name] = check
}

// Start serves the health endpoint at path (plus /version) on port, blocking
// until ctx is cancelled — then it drains with a 5s Shutdown timeout — or
// ListenAndServe fails. Returns nil on clean context-cancelled shutdown.
func (s *Server) Start(ctx context.Context, port int, path string) error {
	mux := http.NewServeMux()
	mux.HandleFunc(path, s.handleHealth)

	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"voice":"%s"}`, version.Voice())
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	s.logger.Info("health server starting",
		zap.Int("port", port),
		zap.String("path", path),
	)

	errChan := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// handleHealth runs every registered check under a 5s deadline and returns the
// aggregated Response. Any failing check makes the overall status unhealthy and
// the HTTP code 503; otherwise 200. Checks run serially under the read lock.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	s.mu.RLock()
	checks := make([]Check, 0, len(s.checks))
	overallStatus := StatusHealthy

	for name, checkFunc := range s.checks {
		check := Check{Name: name, Status: StatusHealthy}

		if err := checkFunc(ctx); err != nil {
			check.Status = StatusUnhealthy
			check.Error = err.Error()
			overallStatus = StatusUnhealthy
		}

		checks = append(checks, check)
	}
	s.mu.RUnlock()

	response := Response{
		Status:    overallStatus,
		Timestamp: time.Now(),
		Checks:    checks,
		Uptime:    time.Since(s.startTime).String(),
		Version:   s.version,
	}

	w.Header().Set("Content-Type", "application/json")
	if overallStatus == StatusUnhealthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error("failed to encode health response", zap.Error(err))
	}
}
