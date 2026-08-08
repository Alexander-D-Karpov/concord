package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Event is a single audit record: who (UserID) did what (Action) to which resource
// (ResourceID/ResourceType), optionally scoped to RoomID, with optional request
// context and free-form Metadata. ID and Timestamp are auto-filled by Log when left
// zero.
type Event struct {
	ID           uuid.UUID
	RoomID       string
	UserID       string
	Action       string
	ResourceID   string
	ResourceType string
	IPAddress    string
	UserAgent    string
	Metadata     map[string]interface{}
	Timestamp    time.Time
}

// Logger records audit events to the audit_log table and mirrors them to the
// structured log.
type Logger struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
}

// NewLogger returns a Logger backed by the given database pool and zap logger.
func NewLogger(pool *pgxpool.Pool, logger *zap.Logger) *Logger {
	return &Logger{
		pool:   pool,
		logger: logger,
	}
}

// Log persists an audit event, assigning a random ID and the current timestamp when
// unset, and also emits it as a structured log line. Metadata is stored as JSONB. A
// nil RoomID/target field is stored as SQL NULL. The event value is passed by value,
// so the caller's copy is not mutated.
func (al *Logger) Log(ctx context.Context, event Event) error {
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	var metadata []byte
	if len(event.Metadata) > 0 {
		b, err := json.Marshal(event.Metadata)
		if err != nil {
			return err
		}
		metadata = b
	}

	_, err := al.pool.Exec(ctx, `
		INSERT INTO audit_log
			(id, room_id, actor_id, action, target_id, target_type, ip_address, user_agent, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		event.ID,
		nullString(event.RoomID),
		event.UserID,
		event.Action,
		nullString(event.ResourceID),
		nullString(event.ResourceType),
		nullString(event.IPAddress),
		nullString(event.UserAgent),
		metadata,
		event.Timestamp,
	)
	if err != nil {
		return err
	}

	al.logger.Info("audit event",
		zap.String("event_id", event.ID.String()),
		zap.String("room_id", event.RoomID),
		zap.String("user_id", event.UserID),
		zap.String("action", event.Action),
		zap.String("resource_id", event.ResourceID),
		zap.String("resource_type", event.ResourceType),
	)

	return nil
}

// List returns audit events for roomID, newest first, paginated by limit and
// offset. A non-positive limit defaults to 50 and is capped at 200.
func (al *Logger) List(ctx context.Context, roomID string, limit, offset int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := al.pool.Query(ctx, `
		SELECT id, room_id, actor_id, action, target_id, target_type, ip_address, user_agent, metadata, created_at
		FROM audit_log
		WHERE room_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`, roomID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var (
			e                                                     Event
			roomIDVal, targetID, targetType, ipAddress, userAgent *string
			metadata                                              []byte
		)
		if err := rows.Scan(
			&e.ID, &roomIDVal, &e.UserID, &e.Action, &targetID, &targetType,
			&ipAddress, &userAgent, &metadata, &e.Timestamp,
		); err != nil {
			return nil, err
		}
		e.RoomID = deref(roomIDVal)
		e.ResourceID = deref(targetID)
		e.ResourceType = deref(targetType)
		e.IPAddress = deref(ipAddress)
		e.UserAgent = deref(userAgent)
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &e.Metadata)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// LogKick records a "user.kick" audit event attributing the kick of targetUserID
// in roomID to adminID.
func (al *Logger) LogKick(ctx context.Context, adminID, targetUserID, roomID string) error {
	return al.Log(ctx, Event{
		RoomID:       roomID,
		UserID:       adminID,
		Action:       "user.kick",
		ResourceID:   targetUserID,
		ResourceType: "user",
	})
}

// LogBan records a "user.ban" audit event attributing the ban of targetUserID in
// roomID (with the given duration) to adminID.
func (al *Logger) LogBan(ctx context.Context, adminID, targetUserID, roomID string, duration int64) error {
	return al.Log(ctx, Event{
		RoomID:       roomID,
		UserID:       adminID,
		Action:       "user.ban",
		ResourceID:   targetUserID,
		ResourceType: "user",
		Metadata: map[string]interface{}{
			"duration": duration,
		},
	})
}

// nullString maps an empty string to nil so it is stored as SQL NULL rather than an
// empty string.
func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// deref returns the pointed-to string, or "" for a nil pointer.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
