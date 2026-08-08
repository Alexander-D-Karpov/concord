// Package message is a surface-agnostic message data layer shared by rooms and DMs.
//
// Core runs the common message operations — create, edit, react, pin, thread,
// query, and paginate — against whichever tables a TableSpec names, so the same
// logic serves both the room and DM schemas. Cross-cutting behavior is injected
// through function seams such as MediaInsertFunc and RecordEditFunc so this package
// does not depend on the media or editing packages directly.
package message
