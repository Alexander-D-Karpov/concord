// Package call is the gRPC control plane for room voice calls (join, leave, media
// preferences, status) and a snapshot pusher for reconnecting clients.
//
// It is a thin layer over voiceassign, with no repository or service of its own.
// Access is gated by one of three tiers — requireVoiceAccess, requireAuthed, or
// requireMember — so pick the right guard per RPC. Snapshotter pushes the full
// voice state of a user's rooms and their active DM calls after a reconnect.
package call
