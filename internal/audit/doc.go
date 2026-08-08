// Package audit records audit events — chiefly moderation actions — to the
// audit_log table.
//
// Logger.Log persists one row (assigning an ID and timestamp when unset) and also
// emits a structured zap line; List reads events back scoped to a room, newest
// first. LogKick and LogBan are convenience wrappers. IDs are stored as TEXT (not
// foreign keys) so audit history survives room/user deletion.
package audit
