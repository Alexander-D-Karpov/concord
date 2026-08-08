// Package session is the authoritative voice session registry, mapping SSRC,
// address, and user-in-room to a *Session and holding per-session media state,
// crypto, and retransmit buffers.
//
// Two lock levels exist: Manager.mu guards the index maps and each Session has its
// own Mu. GetRoomSessions returns an atomic snapshot that is safe to range without
// holding a lock. SweepInactive is two-stage — an idle session is first marked
// inactive (reported once, to drive re-announce) and only later removed, with SSRC
// and crypto preserved in between so a resuming client keeps its identity.
// SetCrypto retains the previous key for rotation overlap, selected on the wire by
// KeyID.
package session
