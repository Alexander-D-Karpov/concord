// Package voiceassign assigns users to voice/media servers, issues voice JWTs, and
// tracks live voice session state.
//
// It load-balances region-aware server selection, mints scoped voice tokens, and
// keeps per-user sessions plus room→server, room→port, and room→crypto mappings in
// an in-memory, mutex-guarded store. Only the sessions are purely ephemeral (lost
// on restart); the room→server pin is also persisted to Postgres and the
// room→crypto suite cached in Redis, so both survive a restart, and room→port is a
// deterministic FNV hash of the room ID. StartHealthChecker is a blocking periodic
// loop (the caller runs it in
// its own goroutine) that marks heartbeat-lapsed servers offline, evicts their
// in-memory sessions, and notifies affected users to rejoin — nothing is re-homed
// in place; reassignment happens when those users reconnect. Depends on rooms and
// events through interfaces (RoomServerAssigner, EventPublisher) for testability.
package voiceassign
