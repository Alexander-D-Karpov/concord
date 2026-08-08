// Package room is a secondary room/session index for the voice server.
//
// Deprecated in practice: it is redundant with session.Manager's room index and is
// never populated with sessions — only GetAllRooms is used, for a health check. It
// is a refactor leftover; the live routing index is session.Manager.
package room
