package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

type HealthStatus string

const (
	StatusHealthy   HealthStatus = "healthy"
	StatusDegraded  HealthStatus = "degraded"
	StatusUnhealthy HealthStatus = "unhealthy"
)

type ComponentHealth struct {
	Status  HealthStatus `json:"status"`
	Message string       `json:"message,omitempty"`
	Latency string       `json:"latency,omitempty"`
}

type HealthResponse struct {
	Status     HealthStatus               `json:"status"`
	Timestamp  time.Time                  `json:"timestamp"`
	Components map[string]ComponentHealth `json:"components"`
	Version    string                     `json:"version"`
	Uptime     string                     `json:"uptime"`
}

type HealthChecker struct {
	checks    map[string]HealthCheck
	logger    *zap.Logger
	startTime time.Time
	version   string
	mu        sync.RWMutex
	server    *http.Server
}

type HealthCheck func(context.Context) (HealthStatus, string, error)

func NewHealthChecker(logger *zap.Logger, version string) *HealthChecker {
	return &HealthChecker{
		checks:    make(map[string]HealthCheck),
		logger:    logger,
		startTime: time.Now(),
		version:   version,
	}
}

func (h *HealthChecker) RegisterCheck(name string, check HealthCheck) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = check
}

func (h *HealthChecker) Start(ctx context.Context, port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/health/ready", h.handleReadiness)
	mux.HandleFunc("/health/live", h.handleLiveness)

	h.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	h.logger.Info("health server starting", zap.Int("port", port))

	errChan := make(chan error, 1)
	go func() {
		if err := h.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return h.Stop(context.Background())
	}
}

func (h *HealthChecker) Stop(ctx context.Context) error {
	if h.server == nil {
		return nil
	}
	return h.server.Shutdown(ctx)
}

func (h *HealthChecker) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	h.mu.RLock()
	checks := make(map[string]HealthCheck, len(h.checks))
	for name, check := range h.checks {
		checks[name] = check
	}
	h.mu.RUnlock()

	components := make(map[string]ComponentHealth)
	overallStatus := StatusHealthy

	for name, check := range checks {
		start := time.Now()
		status, message, err := check(ctx)
		latency := time.Since(start)

		component := ComponentHealth{
			Status:  status,
			Message: message,
			Latency: latency.String(),
		}

		if err != nil {
			component.Message = err.Error()
			component.Status = StatusUnhealthy
		}

		components[name] = component

		if component.Status == StatusUnhealthy {
			overallStatus = StatusUnhealthy
		} else if component.Status == StatusDegraded && overallStatus != StatusUnhealthy {
			overallStatus = StatusDegraded
		}
	}

	response := HealthResponse{
		Status:     overallStatus,
		Timestamp:  time.Now(),
		Components: components,
		Version:    h.version,
		Uptime:     time.Since(h.startTime).String(),
	}

	w.Header().Set("Content-Type", "application/json")

	switch overallStatus {
	case StatusUnhealthy:
		w.WriteHeader(http.StatusServiceUnavailable)
	case StatusDegraded:
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Error("failed to encode health response", zap.Error(err))
	}
}

func (h *HealthChecker) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	h.mu.RLock()
	checks := make(map[string]HealthCheck, len(h.checks))
	for name, check := range h.checks {
		checks[name] = check
	}
	h.mu.RUnlock()

	ready := true
	for _, check := range checks {
		status, _, err := check(ctx)
		if err != nil || status == StatusUnhealthy {
			ready = false
			break
		}
	}

	if ready {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ready")); err != nil {
			return
		}
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := w.Write([]byte("not ready")); err != nil {
			return
		}
	}
}

func (h *HealthChecker) handleLiveness(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("alive")); err != nil {
		return
	}
}
