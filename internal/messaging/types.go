package messaging

import (
	"time"

	"github.com/google/uuid"
)

type Message struct {
	ID          int64
	ChannelID   uuid.UUID
	RoomID      uuid.UUID
	AuthorID    uuid.UUID
	Content     string
	CreatedAt   time.Time
	EditedAt    *time.Time
	DeletedAt   *time.Time
	ReplyToID   *int64
	ReplyCount  int32
	Pinned      bool
	Reactions   []Reaction
	Attachments []Attachment
	Mentions    []uuid.UUID
}

type Reaction struct {
	ID        uuid.UUID
	MessageID int64
	UserID    uuid.UUID
	Emoji     string
	CreatedAt time.Time
}

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

func (m *Message) IsDM() bool {
	return m.ChannelID != uuid.Nil
}

func (m *Message) IsRoom() bool {
	return m.RoomID != uuid.Nil
}
