// Package users manages user profiles, handles, OAuth lookup, avatars, status,
// and in-process presence.
//
// It follows the repository/service/handler split with an optional cached
// repository, and the avatar path runs an image-processing pipeline.
// PresenceManager keeps online/away/offline/dnd state in an in-memory map (not
// Redis), so presence is per-process and lost on restart; status changes are
// broadcast, only when the effective status actually changes, to the user's
// friends and their shared rooms.
package users
