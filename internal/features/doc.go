// Package features is the aggregate "extra messaging features" service: message
// forwards, scheduled messages, bookmarks, drafts, notification overrides, channel
// media, stickers, and GIF search.
//
// RunScheduler is a long-running background loop that delivers scheduled messages
// using a FOR UPDATE-style claim (ClaimNextScheduledMessage) for safe concurrent
// delivery, with recovery and failure semantics for stuck rows. Aggregator
// composes newer per-feature services (polls, slow mode) over this legacy
// monolithic Service to present one gRPC surface.
package features
