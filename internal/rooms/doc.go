// Package rooms handles room CRUD, membership storage (members, roles,
// nicknames), room invites, and voice-server attachment.
//
// It owns the room, membership, and room-invite tables (role and nickname are
// columns on the membership table, not separate tables) even though the
// membership package holds the invite/role business logic on top of this
// repository. Caching is active only when built with NewRepositoryWithCache; a
// member change must invalidate the member, member-list, and user-rooms cache
// keys together (the room record cache is untouched).
package rooms
