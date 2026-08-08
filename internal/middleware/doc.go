// Package middleware holds cross-cutting gRPC interceptors — panic recovery,
// request logging, per-call timeouts, and request validation — plus an HTTP gzip
// middleware.
//
// TimeoutInterceptor applies a default deadline that longTimeoutMethods overrides
// per method; quietMethodPrefixes suppress logging for noisy RPCs (health,
// heartbeat). ValidationInterceptor only validates requests that implement the
// Validator interface.
package middleware
