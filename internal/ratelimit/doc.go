// Package ratelimit provides distributed, per-category request rate limiting for
// gRPC, backed by a Redis token-bucket Lua script with an in-memory fallback.
//
// Limiter classifies each method into a Category (auth, message, upload, read, …)
// and enforces its bucket; when Redis is unavailable it degrades to per-instance
// local limiting. When enabled, NewLimiter starts a background cleanup goroutine,
// so callers must call Close (which is a safe no-op otherwise). A request carrying the configured bypass token skips limiting,
// but the token is only honored when voice-debug mode is on.
package ratelimit
