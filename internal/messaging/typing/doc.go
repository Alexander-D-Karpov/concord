// Package typing provides ephemeral "user is typing" indicators for rooms and DMs.
//
// Indicators are persisted with an expiry and must be reaped periodically via
// CleanupExpired (concord-api runs this every 2s). A per-(user, target) rate limiter (2s)
// lives in an in-memory map guarded by a mutex, with an ad-hoc sweep past 10,000
// entries, so it is per-process and not durable. DM typing is broadcast per
// recipient.
package typing
