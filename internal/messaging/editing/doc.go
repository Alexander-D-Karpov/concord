// Package editing records and reads message edit history for both rooms and DMs.
//
// Recorder's write methods require a caller-supplied pgx.Tx and must run inside
// the same transaction that updates the message, or the history desyncs; the
// version number is computed as MAX(version)+1, which can race under concurrent
// edits without row locking. Room and DM edits live in separate tables, read back
// through Reader.
package editing
