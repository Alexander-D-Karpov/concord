// Package chat implements room (channel) text messaging: send, edit, delete,
// reactions, pins, threads, search, and mentions.
//
// It follows the repository/service/handler split (its handler file is
// handlers.go). Message IDs are int64 Snowflakes, not UUIDs. SendMessage enforces
// slow mode via slowmode.CheckAndStamp and, as a side effect, parses mentions and
// broadcasts mention notifications; edit-history recording happens inside the
// repository's core edit transaction (via the RecordEdit hook wired in
// NewRepository), not in the service.
package chat
