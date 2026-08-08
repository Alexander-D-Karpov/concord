package media

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Indexer writes media_index rows within a caller-supplied transaction; it is
// stateless.
type Indexer struct{}

// NewIndexer returns a ready-to-use Indexer.
func NewIndexer() *Indexer { return &Indexer{} }

// mediaTypeFor maps a MIME content type to the media_index media_type code:
// 1 for image/*, 2 for video/*, and 3 for everything else.
func mediaTypeFor(contentType string) int16 {
	switch {
	case len(contentType) >= 6 && contentType[:6] == "image/":
		return 1
	case len(contentType) >= 6 && contentType[:6] == "video/":
		return 2
	default:
		return 3
	}
}

// InsertRoomTx records one room message attachment in media_index within tx,
// deriving media_type from mimeType and storing NULL for a zero width/height.
func (i *Indexer) InsertRoomTx(ctx context.Context, tx pgx.Tx, messageID int64, roomID uuid.UUID, url, mimeType string, width, height int, size int64, createdAt time.Time) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO media_index (message_id, room_id, media_type, file_url, mime_type, width, height, size_bytes, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		messageID, roomID, mediaTypeFor(mimeType), url, mimeType, nilIfZero(width), nilIfZero(height), size, createdAt)
	return err
}

// InsertChannelTx records one DM (channel) message attachment in media_index
// within tx, mirroring InsertRoomTx but keyed by channel_id.
func (i *Indexer) InsertChannelTx(ctx context.Context, tx pgx.Tx, messageID int64, channelID uuid.UUID, url, mimeType string, width, height int, size int64, createdAt time.Time) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO media_index (message_id, channel_id, media_type, file_url, mime_type, width, height, size_bytes, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		messageID, channelID, mediaTypeFor(mimeType), url, mimeType, nilIfZero(width), nilIfZero(height), size, createdAt)
	return err
}

// nilIfZero returns nil for a zero value so it is stored as SQL NULL, and a
// pointer to v otherwise; used for optional width/height dimensions.
func nilIfZero(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
