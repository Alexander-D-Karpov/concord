package message

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/messaging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rowScanner abstracts pgx.Row and pgx.Rows so scanCore can read a single row
// from either a QueryRow result or a Rows iteration.
type rowScanner interface {
	Scan(dest ...any) error
}

// MediaInsertFunc is a seam invoked once per attachment inside Create's
// transaction to mirror the attachment into the media subsystem, keeping this
// package decoupled from it. It runs on tx, so a returned error aborts the whole
// insert.
type MediaInsertFunc func(ctx context.Context, tx pgx.Tx, messageID int64, scopeID uuid.UUID, url, mimeType string, width, height int, size int64, createdAt time.Time) error

// RecordEditFunc is a seam invoked inside Edit's transaction to append the prior
// content to edit history before the row is updated. It runs on tx so history and
// message stay consistent.
type RecordEditFunc func(ctx context.Context, tx pgx.Tx, messageID int64, previousContent string) error

// TableSpec names the concrete tables and columns of one surface (room or DM) and
// carries its injected behavior, letting a single Core operate against either
// schema. ScopeColumn/ForwardScopeColumn hold the surface's scope FK (room_id vs
// channel_id); MaxAttachments of 0 disables the attachment-count check.
type TableSpec struct {
	Surface            messaging.Surface
	Messages           string
	Attachments        string
	Reactions          string
	Mentions           string
	Pinned             string
	PinnedFK           string
	ScopeColumn        string
	ForwardScopeColumn string
	MaxAttachments     int
	MediaInsert        MediaInsertFunc
	RecordEdit         RecordEditFunc
}

// selectColumns builds the aliased SELECT list (rows aliased m) for a full
// message row, including a correlated subquery deriving the pinned flag from the
// pinned table. The order must match scanCore's Scan.
func (s TableSpec) selectColumns() string {
	return fmt.Sprintf(`
	m.id,
	m.%[1]s,
	m.author_id,
	m.content,
	m.created_at,
	m.edited_at,
	m.deleted_at,
	m.reply_to_id,
	m.reply_count,
	COALESCE((SELECT true FROM %[2]s WHERE message_id = m.id), false) as pinned,
	m.forwarded_from_user_id,
	m.forwarded_from_user_name,
	m.%[3]s,
	m.forwarded_from_message_id,
	m.forwarded_original_timestamp,
	m.media_group_id,
	m.reply_quoted_content,
	COALESCE(m.reply_mention_author, true),
	COALESCE(m.edit_count, 0)
`, s.ScopeColumn, s.Pinned, s.ForwardScopeColumn)
}

// scanCore scans one selectColumns row into m, setting Surface and routing the
// single scope/forward-scope columns into the surface-appropriate fields
// (RoomID/ForwardFromRoomID or ChannelID/ForwardFromChannelID). It does not load
// reactions, attachments, or mentions.
func (s TableSpec) scanCore(sc rowScanner, m *messaging.Message) error {
	m.Surface = s.Surface
	var scope uuid.UUID
	var fwdScope *uuid.UUID
	if err := sc.Scan(
		&m.ID,
		&scope,
		&m.AuthorID,
		&m.Content,
		&m.CreatedAt,
		&m.EditedAt,
		&m.DeletedAt,
		&m.ReplyToID,
		&m.ReplyCount,
		&m.Pinned,
		&m.ForwardFromUserID,
		&m.ForwardFromUserName,
		&fwdScope,
		&m.ForwardFromMsgID,
		&m.ForwardOriginalTS,
		&m.MediaGroupID,
		&m.ReplyQuotedContent,
		&m.ReplyMentionAuthor,
		&m.EditCount,
	); err != nil {
		return err
	}
	switch s.Surface {
	case messaging.SurfaceRoom:
		m.RoomID = &scope
		m.ForwardFromRoomID = fwdScope
	case messaging.SurfaceDM:
		m.ChannelID = &scope
		m.ForwardFromChannelID = fwdScope
	}
	return nil
}

// Core is the shared message data layer bound to one surface's TableSpec. Its
// methods run create/edit/react/pin/thread/query/paginate operations against the
// spec's tables using pool.
type Core struct {
	pool *pgxpool.Pool
	spec TableSpec
}

// NewCore returns a Core bound to pool and the given surface TableSpec.
func NewCore(pool *pgxpool.Pool, spec TableSpec) *Core {
	return &Core{pool: pool, spec: spec}
}

// SelectColumns exposes the spec's message SELECT list for callers that build
// custom queries needing scanCore-compatible columns.
func (c *Core) SelectColumns() string { return c.spec.selectColumns() }

// MessagesTable returns the name of this surface's messages table.
func (c *Core) MessagesTable() string { return c.spec.Messages }

// Keep the pgx import referenced even if pgx.ErrNoRows is only used elsewhere.
var _ = pgx.ErrNoRows
