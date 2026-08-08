// Package admin implements room moderation: kick, ban, unban, and mute, listing the
// active bans/mutes and the room's audit log, and reading/writing per-room settings.
//
// Every action re-checks the caller's admin (ban/unban/kick, settings update) or
// moderator (mute, list, settings read) role from the database before acting,
// broadcasts the result through the events Hub, and writes an audit record via the
// injected audit logger (nil-safe). Ban and mute storage is delegated to the rooms
// repository so bans are enforced at the invite-accept and voice-join paths, not
// just recorded here. Room settings are persisted through rooms.Repository and
// enforced in membership, chat, and the retention purger.
package admin
