package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
)

type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

type Response struct {
	Status    Status    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Checks    []Check   `json:"checks"`
	Uptime    string    `json:"uptime"`
}

type Server struct {
	logger    *zap.Logger
	startTime time.Time
	checks    map[string]func(context.Context) error
	mu        sync.RWMutex
}

func NewServer(logger *zap.Logger) *Server {
	return &Server{
		logger:    logger,
		startTime: time.Now(),
		checks:    make(map[string]func(context.Context) error),
	}
}

func (s *Server) RegisterCheck(name string, check func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[name] = check
}

func (s *Server) Start(ctx context.Context, port int, path string) error {
	mux := http.NewServeMux()
	mux.HandleFunc(path, s.handleHealth)

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
