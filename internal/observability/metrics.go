package observability

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

type Metrics struct {
	requestsTotal     *prometheus.CounterVec
	requestDuration   *prometheus.HistogramVec
	activeConnections prometheus.Gauge
	dbQueryDuration   *prometheus.HistogramVec
	cacheHits         *prometheus.CounterVec
	errorTotal        *prometheus.CounterVec
	logger            *zap.Logger
}

func NewMetrics(logger *zap.Logger) *Metrics {
	m := &Metrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "concord_api_requests_total",
				Help: "Total number of API requests",
			},
			[]string{"method", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "concord_api_request_duration_seconds",
				Help:    "API request duration in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method"},
		),
		activeConnections: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "concord_api_active_connections",
				Help: "Number of active gRPC connections",
			},
		),
		dbQueryDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "concord_api_db_query_duration_seconds",
				Help:    "Database query duration in seconds",
				Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
			},
			[]string{"query_type"},
		),
		cacheHits: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "concord_api_cache_hits_total",
				Help: "Total number of cache hits/misses",
			},
			[]string{"cache_type", "status"},
		),
		errorTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "concord_api_errors_total",
				Help: "Total number of errors",
			},
			[]string{"type", "method"},
		),
		logger: logger,
	}

	prometheus.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.activeConnections,
		m.dbQueryDuration,
		m.cacheHits,
		m.errorTotal,
	)

	return m
}

func (m *Metrics) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		start := time.Now()
		m.activeConnections.Inc()
		defer m.activeConnections.Dec()

		resp, err := handler(ctx, req)

		duration := time.Since(start).Seconds()
		statusCode := "success"
		if err != nil {
			st := status.Convert(err)
			statusCode = st.Code().String()
			m.errorTotal.WithLabelValues(statusCode, info.FullMethod).Inc()
		}

		m.requestsTotal.WithLabelValues(info.FullMethod, statusCode).Inc()
		m.requestDuration.WithLabelValues(info.FullMethod).Observe(duration)

		return resp, err
	}
}

func (m *Metrics) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		start := time.Now()
		m.activeConnections.Inc()
		defer m.activeConnections.Dec()

		err := handler(srv, ss)

		duration := time.Since(start).Seconds()
		statusCode := "success"
		if err != nil {
			st := status.Convert(err)
			statusCode = st.Code().String()
			m.errorTotal.WithLabelValues(statusCode, info.FullMethod).Inc()
		}

		m.requestsTotal.WithLabelValues(info.FullMethod, statusCode).Inc()
		m.requestDuration.WithLabelValues(info.FullMethod).Observe(duration)

		return err
	}
}

func (m *Metrics) RecordDBQuery(queryType string, duration time.Duration) {
	m.dbQueryDuration.WithLabelValues(queryType).Observe(duration.Seconds())
}

func (m *Metrics) RecordCacheHit(cacheType string, hit bool) {
	status := "hit"
	if !hit {
		status = "miss"
	}
	m.cacheHits.WithLabelValues(cacheType, status).Inc()
}

func (m *Metrics) Start(ctx context.Context, port int) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	m.logger.Info("metrics server starting", zap.Int("port", port))

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
