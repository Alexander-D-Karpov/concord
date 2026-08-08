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

// HealthStatus is the health level reported for a component or the service as a
// whole.
type HealthStatus string

const (
	// StatusHealthy indicates the component is fully operational.
	StatusHealthy HealthStatus = "healthy"
	// StatusDegraded indicates the component is impaired but still serving.
	StatusDegraded HealthStatus = "degraded"
	// StatusUnhealthy indicates the component is failing; it drives the overall
	// status to unhealthy and readiness to not-ready.
	StatusUnhealthy HealthStatus = "unhealthy"
)

// ComponentHealth is the health of a single registered component, including an
// optional message and the latency of its most recent check.
type ComponentHealth struct {
	Status  HealthStatus `json:"status"`
	Message string       `json:"message,omitempty"`
	Latency string       `json:"latency,omitempty"`
}

// HealthResponse is the JSON body returned by the /health endpoint: the overall
// status plus per-component detail, version, and process uptime.
type HealthResponse struct {
	Status     HealthStatus               `json:"status"`
	Timestamp  time.Time                  `json:"timestamp"`
	Components map[string]ComponentHealth `json:"components"`
	Version    string                     `json:"version"`
	Uptime     string                     `json:"uptime"`
}

// HealthChecker runs a set of named HealthCheck functions and exposes the
// results over HTTP (/health, /health/ready, /health/live). Registered checks
// are guarded by mu so they can be added while the server is serving.
type HealthChecker struct {
	checks    map[string]HealthCheck
	logger    *zap.Logger
	startTime time.Time
	version   string
	mu        sync.RWMutex
	server    *http.Server
}

// HealthCheck probes one component and returns its status, a human-readable
// message, and an error; a non-nil error is treated as unhealthy.
type HealthCheck func(context.Context) (HealthStatus, string, error)

// NewHealthChecker returns a HealthChecker with no checks registered, recording
// the current time as the start point for uptime reporting.
func NewHealthChecker(logger *zap.Logger, version string) *HealthChecker {
	return &HealthChecker{
		checks:    make(map[string]HealthCheck),
		logger:    logger,
		startTime: time.Now(),
		version:   version,
	}
}

// RegisterCheck adds or replaces the check registered under name. It is safe to
// call concurrently with the HTTP handlers.
func (h *HealthChecker) RegisterCheck(name string, check HealthCheck) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = check
}

// Start serves the health endpoints on port and blocks until the server fails
// or ctx is cancelled, at which point it shuts the server down gracefully. A
// clean ErrServerClosed is not reported as an error.
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

// Stop gracefully shuts down the health HTTP server, or returns nil if it was
// never started.
func (h *HealthChecker) Stop(ctx context.Context) error {
	if h.server == nil {
		return nil
	}
	return h.server.Shutdown(ctx)
}

// handleHealth runs every registered check under a 5s deadline and writes the
// aggregated HealthResponse as JSON. The overall status is the worst component
// status; only an unhealthy overall yields HTTP 503, while healthy and degraded
// both return 200.
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

// handleReadiness runs the checks under a 2s deadline and returns 200 "ready"
// only if every check passes; any error or unhealthy result yields 503 "not
// ready" so load balancers stop routing traffic.
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

// handleLiveness always returns 200 "alive" without running any checks; it
// reports only that the process is up, so a live-but-unready server is not
// killed by liveness probes.
func (h *HealthChecker) handleLiveness(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("alive")); err != nil {
		return
	}
}
