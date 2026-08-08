// Package observability provides health/readiness endpoints, Prometheus metrics,
// and request-ID gRPC interceptors.
//
// HealthChecker and Metrics each run their own HTTP server on a separate port
// (health/readiness/livez and /metrics respectively). RequestIDInterceptor
// generates a request ID when absent and propagates correlation IDs through the
// context for downstream logging.
package observability
