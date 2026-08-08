package messaging

import (
	"time"

	"github.com/google/uuid"
)

// Surface identifies which product surface a Message belongs to (room, DM, or
// unknown). It selects the scoping field to read (RoomID vs ChannelID) and the
// tables/proto shape used elsewhere.
type Surface int

const (
	// SurfaceUnknown is the zero value, meaning the surface was not set.
	SurfaceUnknown Surface = 0
	// SurfaceRoom marks a message scoped to a room (RoomID is populated).
	SurfaceRoom Surface = 1
	// SurfaceDM marks a message scoped to a DM channel (ChannelID is populated).
	SurfaceDM Surface = 2
)

// Message is the surface-agnostic, superset message view shared by rooms and DMs.
// Surface tags which surface it belongs to; RoomID and ChannelID are mutually
// exclusive per Surface, so prefer SurfaceID over reading them directly. Optional
// features (reply, forward, media group, reactions, mentions, read receipts) are
// carried as pointers/slices left nil or empty when unused.
type Message struct {
	ID        int64
	Surface   Surface
	RoomID    *uuid.UUID
	ChannelID *uuid.UUID
	AuthorID  uuid.UUID
	Content   string
	CreatedAt time.Time
	EditedAt  *time.Time
	DeletedAt *time.Time

	ReplyToID          *int64
	ReplyCount         int32
	ReplyQuotedContent *string
	ReplyMentionAuthor bool

	Pinned    bool
	EditCount int32

	ForwardFromUserID    *uuid.UUID
	ForwardFromUserName  *string
	ForwardFromRoomID    *uuid.UUID
	ForwardFromChannelID *uuid.UUID
	ForwardFromMsgID     *int64
	ForwardOriginalTS    *time.Time

	MediaGroupID *string

	Reactions   []Reaction
	Attachments []Attachment
	Mentions    []uuid.UUID
	ReadBy      []ReadReceipt
}

// Reaction is a single emoji reaction placed on a message by one user.
type Reaction struct {
	ID        uuid.UUID
	MessageID int64
	UserID    uuid.UUID
	Emoji     string
	CreatedAt time.Time
}

// Attachment is a media file attached to a message; Size is in bytes and Width/
// Height are in pixels (0 for non-image attachments).
type Attachment struct {
	ID          uuid.UUID
	MessageID   int64
	URL         string
	Filename    string
	ContentType string
	Size        int64
	Width       int
	Height      int
	CreatedAt   time.Time
}

// ReadReceipt records that a user read a message at a given time (used for DM
// read state).
type ReadReceipt struct {
	UserID uuid.UUID
	ReadAt time.Time
}

// IsDM reports whether the message is scoped to a DM channel.
func (m *Message) IsDM() bool { return m.Surface == SurfaceDM }

// IsRoom reports whether the message is scoped to a room.
func (m *Message) IsRoom() bool { return m.Surface == SurfaceRoom }

// SurfaceID returns the scoping ID for the message's surface: ChannelID for DMs,
// RoomID for rooms. It returns uuid.Nil when the surface is unknown or the
// matching ID is unset.
func (m *Message) SurfaceID() uuid.UUID {
	if m.Surface == SurfaceDM && m.ChannelID != nil {
		return *m.ChannelID
	}
	if m.Surface == SurfaceRoom && m.RoomID != nil {
		return *m.RoomID
	}
	return uuid.Nil
}
