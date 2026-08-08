// Package messaging holds the surface-agnostic message domain types shared across
// rooms and DMs, plus proto conversion helpers.
//
// Its unified Message is a superset carrying reply/forward/media-group/reaction/
// mention/read fields, tagged by Surface (Room, DM, or Unknown); RoomID and
// ChannelID are mutually exclusive, so use SurfaceID rather than reading them
// directly. This type is distinct from the domain-specific chat.Message and
// dm.DMMessage — it is the cross-surface view. The subpackages implement the
// individual message features.
package messaging
