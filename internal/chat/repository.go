package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/Alexander-D-Karpov/concord/internal/messaging"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/editing"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/media"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/message"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Message is a room (channel) message in this package's domain shape. ID is an
// int64 Snowflake whose embedded timestamp also drives CreatedAt. EditedAt and
// DeletedAt are nil until the message is edited or soft-deleted; the Forward*
// and Reply* fields carry optional forward/reply metadata. It converts to and
// from the shared messaging.Message via toCore/fromCore.
type Message struct {
	ID          int64
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

	ForwardFromUserID   *uuid.UUID
	ForwardFromUserName *string
	ForwardFromRoomID   *uuid.UUID
	ForwardFromMsgID    *int64
	ForwardOriginalTS   *time.Time
	MediaGroupID        *string
	ReplyQuotedContent  *string
	ReplyMentionAuthor  bool
	EditCount           int32
}

// Reaction is a single emoji reaction by one user on a message. The
// (MessageID, UserID, Emoji) triple is unique, so a user can react with a given
// emoji at most once.
type Reaction struct {
	ID        uuid.UUID
	MessageID int64
	UserID    uuid.UUID
	Emoji     string
	CreatedAt time.Time
}

// Attachment is a stored file (image, etc.) linked to a message. Width and
// Height are zero for non-image content.
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

// Repository is the persistence layer for room messages. Most operations are
// delegated to a shared message.Core configured for the room surface; this type
// adds room-specific concerns (Snowflake IDs, mention rows, slow-mode lookups).
type Repository struct {
	pool      *pgxpool.Pool
	snowflake *infra.SnowflakeGenerator
	core      *message.Core
}

// NewRepository builds a room-message Repository, wiring a message.Core with the
// room table layout (messages/attachments/reactions/mentions/pins) and the media
// indexer and edit recorder used inside the core's write transactions.
func NewRepository(pool *pgxpool.Pool, snowflake *infra.SnowflakeGenerator) *Repository {
	idx := media.NewIndexer()
	rec := editing.NewRecorder()
	spec := message.TableSpec{
		Surface:            messaging.SurfaceRoom,
		Messages:           "messages",
		Attachments:        "message_attachments",
		Reactions:          "message_reactions",
		Mentions:           "message_mentions",
		Pinned:             "pinned_messages",
		PinnedFK:           "room_id",
		ScopeColumn:        "room_id",
		ForwardScopeColumn: "forwarded_from_room_id",
		MaxAttachments:     MaxAttachmentsPerMessage,
		MediaInsert:        idx.InsertRoomTx,
		RecordEdit:         rec.RecordRoom,
	}
	return &Repository{pool: pool, snowflake: snowflake, core: message.NewCore(pool, spec)}
}

// toCore projects a package Message onto the shared messaging.Message the
// message.Core operates on, tagging it with the room surface and moving RoomID
// into the surface-generic scope pointer.
func toCore(m *Message) *messaging.Message {
	rid := m.RoomID
	core := &messaging.Message{
		ID:                  m.ID,
		Surface:             messaging.SurfaceRoom,
		RoomID:              &rid,
		AuthorID:            m.AuthorID,
		Content:             m.Content,
		CreatedAt:           m.CreatedAt,
		EditedAt:            m.EditedAt,
		DeletedAt:           m.DeletedAt,
		ReplyToID:           m.ReplyToID,
		ReplyCount:          m.ReplyCount,
		ReplyQuotedContent:  m.ReplyQuotedContent,
		ReplyMentionAuthor:  m.ReplyMentionAuthor,
		Pinned:              m.Pinned,
		EditCount:           m.EditCount,
		ForwardFromUserID:   m.ForwardFromUserID,
		ForwardFromUserName: m.ForwardFromUserName,
		ForwardFromRoomID:   m.ForwardFromRoomID,
		ForwardFromMsgID:    m.ForwardFromMsgID,
		ForwardOriginalTS:   m.ForwardOriginalTS,
		MediaGroupID:        m.MediaGroupID,
		Mentions:            m.Mentions,
	}
	for _, a := range m.Attachments {
		core.Attachments = append(core.Attachments, messaging.Attachment(a))
	}
	for _, r := range m.Reactions {
		core.Reactions = append(core.Reactions, messaging.Reaction(r))
	}
	return core
}

// fromCore is the inverse of toCore, rebuilding a package Message from a core
// result and flattening the scope pointer back into RoomID (nil scope yields the
// zero UUID).
func fromCore(c *messaging.Message) *Message {
	m := &Message{
		ID:                  c.ID,
		AuthorID:            c.AuthorID,
		Content:             c.Content,
		CreatedAt:           c.CreatedAt,
		EditedAt:            c.EditedAt,
		DeletedAt:           c.DeletedAt,
		ReplyToID:           c.ReplyToID,
		ReplyCount:          c.ReplyCount,
		Pinned:              c.Pinned,
		Mentions:            c.Mentions,
		ForwardFromUserID:   c.ForwardFromUserID,
		ForwardFromUserName: c.ForwardFromUserName,
		ForwardFromRoomID:   c.ForwardFromRoomID,
		ForwardFromMsgID:    c.ForwardFromMsgID,
		ForwardOriginalTS:   c.ForwardOriginalTS,
		MediaGroupID:        c.MediaGroupID,
		ReplyQuotedContent:  c.ReplyQuotedContent,
		ReplyMentionAuthor:  c.ReplyMentionAuthor,
		EditCount:           c.EditCount,
	}
	if c.RoomID != nil {
		m.RoomID = *c.RoomID
	}
	for _, a := range c.Attachments {
		m.Attachments = append(m.Attachments, Attachment(a))
	}
	for _, r := range c.Reactions {
		m.Reactions = append(m.Reactions, Reaction(r))
	}
	return m
}

// Create inserts a message and its attachments in one core transaction.
// It generates a Snowflake ID when none is set and derives CreatedAt from that
// ID's embedded timestamp (not the DB clock), then copies the core-assigned
// attachment IDs/timestamps back onto msg.
func (r *Repository) Create(ctx context.Context, msg *Message) error {
	if msg.ID == 0 {
		msg.ID = r.snowflake.Generate()
	}
	msg.CreatedAt = r.snowflake.ExtractTimestamp(msg.ID)

	core := toCore(msg)
	if err := r.core.Create(ctx, core); err != nil {
		return err
	}
	for i := range core.Attachments {
		msg.Attachments[i].ID = core.Attachments[i].ID
		msg.Attachments[i].MessageID = core.Attachments[i].MessageID
		msg.Attachments[i].CreatedAt = core.Attachments[i].CreatedAt
	}
	return nil
}

// GetByID loads a single message with its attachments, reactions, and mentions.
// It does not filter by room; callers that need room scoping compare RoomID
// themselves.
func (r *Repository) GetByID(ctx context.Context, id int64) (*Message, error) {
	c, err := r.core.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return fromCore(c), nil
}

// ListByRoom returns a page of a room's messages, using beforeID/afterID as
// exclusive Snowflake cursors for backward/forward paging. limit is clamped to
// (0,100] and defaults to 50; results come back in ascending ID order.
func (r *Repository) ListByRoom(ctx context.Context, roomID uuid.UUID, beforeID, afterID *int64, limit int) ([]*Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	cs, err := r.core.List(ctx, roomID, beforeID, afterID, limit, true)
	if err != nil {
		return nil, err
	}
	out := make([]*Message, len(cs))
	for i, c := range cs {
		out[i] = fromCore(c)
	}
	return out, nil
}

// Update applies an edit to a message and refreshes msg.EditedAt/EditCount from
// the result. The *editing.Recorder argument is intentionally ignored: the
// previous-content snapshot is recorded inside the core's edit transaction (via
// the RecordEdit hook configured in NewRepository), so passing a recorder here
// would double-record.
func (r *Repository) Update(ctx context.Context, msg *Message, _ *editing.Recorder) error {
	core := toCore(msg)
	if err := r.core.Edit(ctx, core); err != nil {
		return err
	}
	msg.EditedAt = core.EditedAt
	msg.EditCount = core.EditCount
	return nil
}

// SoftDelete marks a message deleted (sets deleted_at) without removing the row,
// so history and reply chains stay intact.
func (r *Repository) SoftDelete(ctx context.Context, id int64) error {
	return r.core.SoftDelete(ctx, id)
}

// AddReaction records one user's emoji reaction and returns it. The underlying
// insert is idempotent on (message, user, emoji); the concrete duplicate/error
// semantics are defined by message.Core.
func (r *Repository) AddReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (*Reaction, error) {
	cr, err := r.core.AddReaction(ctx, messageID, userID, emoji)
	if err != nil {
		return nil, err
	}
	out := Reaction(*cr)
	return &out, nil
}

// RemoveReaction deletes a user's emoji reaction and returns the removed
// reaction's ID so callers can broadcast the removal.
func (r *Repository) RemoveReaction(ctx context.Context, messageID int64, userID uuid.UUID, emoji string) (uuid.UUID, error) {
	return r.core.RemoveReaction(ctx, messageID, userID, emoji)
}

// PinMessage pins a message in a room, recording who pinned it. The pin lives in
// a separate pinned-messages table keyed by room, not on the message row.
func (r *Repository) PinMessage(ctx context.Context, roomID uuid.UUID, messageID int64, pinnedBy uuid.UUID) error {
	return r.core.Pin(ctx, roomID, messageID, pinnedBy)
}

// UnpinMessage removes a room's pin for the given message.
func (r *Repository) UnpinMessage(ctx context.Context, roomID uuid.UUID, messageID int64) error {
	return r.core.Unpin(ctx, roomID, messageID)
}

// ListPinnedMessages returns a room's pinned messages, newest pin first.
func (r *Repository) ListPinnedMessages(ctx context.Context, roomID uuid.UUID) ([]*Message, error) {
	cs, err := r.core.ListPinned(ctx, roomID, false)
	if err != nil {
		return nil, err
	}
	out := make([]*Message, len(cs))
	for i, c := range cs {
		out[i] = fromCore(c)
	}
	return out, nil
}

// GetThreadReplies returns the replies to parentID (messages whose reply_to is
// parentID), paginated by limit/offset in ascending ID order.
func (r *Repository) GetThreadReplies(ctx context.Context, parentID int64, limit, offset int) ([]*Message, error) {
	cs, err := r.core.Thread(ctx, parentID, limit, offset, false)
	if err != nil {
		return nil, err
	}
	out := make([]*Message, len(cs))
	for i, c := range cs {
		out[i] = fromCore(c)
	}
	return out, nil
}

// Search runs a full-text search within a room. The raw query is split by
// parseSearchQuery into an optional "from:handle" author filter and the
// remaining text, which is matched against the message search vector via
// plainto_tsquery. Deleted messages are excluded; results are newest-first.
func (r *Repository) Search(ctx context.Context, roomID uuid.UUID, query string, limit int) ([]*Message, error) {
	parsed := parseSearchQuery(query)

	conditions := []string{"m.room_id = $1", "m.deleted_at IS NULL"}
	args := []any{roomID}
	argIdx := 2

	if parsed.FTSQuery != "" {
		conditions = append(conditions, fmt.Sprintf("m.search_vector @@ plainto_tsquery('simple', $%d)", argIdx))
		args = append(args, parsed.FTSQuery)
		argIdx++
	}
	if parsed.FromHandle != "" {
		conditions = append(conditions, fmt.Sprintf(
			"m.author_id = (SELECT id FROM users WHERE lower(handle) = lower($%d) LIMIT 1)", argIdx))
		args = append(args, parsed.FromHandle)
		argIdx++
	}

	q := fmt.Sprintf(`SELECT %s FROM %s m WHERE %s ORDER BY m.id DESC LIMIT $%d`,
		r.core.SelectColumns(), r.core.MessagesTable(), strings.Join(conditions, " AND "), argIdx)
	args = append(args, limit)

	cs, err := r.core.QueryAndLoad(ctx, q, true, args...)
	if err != nil {
		return nil, err
	}
	out := make([]*Message, len(cs))
	for i, c := range cs {
		out[i] = fromCore(c)
	}
	return out, nil
}

// parsedSearch is a search string decomposed into its full-text portion
// (FTSQuery) and an optional author filter (FromHandle).
type parsedSearch struct {
	FTSQuery   string
	FromHandle string
}

// parseSearchQuery extracts a leading-or-embedded "from:<handle>" token as the
// author filter and joins the rest as the free-text query. The handle match is
// case-insensitive; only the last from: token wins.
func parseSearchQuery(raw string) parsedSearch {
	p := parsedSearch{}
	var remaining []string
	for _, part := range strings.Fields(raw) {
		if strings.HasPrefix(strings.ToLower(part), "from:") {
			p.FromHandle = strings.TrimPrefix(part, "from:")
		} else {
			remaining = append(remaining, part)
		}
	}
	p.FTSQuery = strings.Join(remaining, " ")
	return p
}

// CreateMentions inserts mention rows linking a message to the mentioned users,
// skipping duplicates (ON CONFLICT DO NOTHING). It is a no-op for an empty list.
func (r *Repository) CreateMentions(ctx context.Context, messageID int64, userIDs []uuid.UUID) error {
	if len(userIDs) == 0 {
		return nil
	}
	for _, userID := range userIDs {
		if _, err := r.pool.Exec(ctx,
			`INSERT INTO message_mentions (message_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			messageID, userID); err != nil {
			return err
		}
	}
	return nil
}

// IncrementReplyCount bumps a parent message's cached reply_count by one; call
// it when a reply is created so thread counts stay accurate without a subquery.
func (r *Repository) IncrementReplyCount(ctx context.Context, messageID int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE messages SET reply_count = reply_count + 1 WHERE id = $1`, messageID)
	return err
}

// GetRoomSlowMode returns a room's slow-mode interval in seconds (0 = disabled),
// returning NotFound when the room does not exist.
func (r *Repository) GetRoomSlowMode(ctx context.Context, roomID uuid.UUID) (int, error) {
	var slowMode int
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(slow_mode_interval, 0) FROM rooms WHERE id = $1`, roomID,
	).Scan(&slowMode)
	if err == pgx.ErrNoRows {
		return 0, errors.NotFound("room not found")
	}
	if err != nil {
		return 0, err
	}
	return slowMode, nil
}
