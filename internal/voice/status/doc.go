// Package status serves an authenticated JSON API exposing voice rooms, room
// detail, and stats for operators and clients.
//
// It authenticates with a normal access token (ValidateAccessToken), unlike the
// UDP data plane which uses voice tokens (ValidateVoiceToken) — two different token
// types. CORS is wide open. Its /v1/voice/health route is trivial and
// unauthenticated; the real health server is the health package.
package status
