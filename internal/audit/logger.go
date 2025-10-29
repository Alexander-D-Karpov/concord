package audit

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type Event struct {
	ID           uuid.UUID
	UserID       string
	Action       string
	ResourceID   string
	ResourceType string
	IPAddress    string
	UserAgent    string
	Metadata     map[string]interface{}
	Timestamp    time.Time
}

type Logger struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
}

func NewLogger(pool *pgxpool.Pool, logger *zap.Logger) *Logger {
	return &Logger{
		pool:   pool,
		logger: logger,
	}
}

func (al *Logger) Log(ctx context.Context, event Event) error {
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	al.logger.Info("audit event",
		zap.String("event_id", event.ID.String()),
		zap.String("user_id", event.UserID),
		zap.String("action", event.Action),
		zap.String("resource_id", event.ResourceID),
		zap.String("resource_type", event.ResourceType),
	)

	return nil
}

func (al *Logger) LogKick(ctx context.Context, adminID, targetUserID, roomID string) error {
	return al.Log(ctx, Event{
		UserID:       adminID,
		Action:       "user.kick",
		ResourceID:   targetUserID,
		ResourceType: "user",
		Metadata: map[string]interface{}{
			"room_id": roomID,
		},
	})
}

func (al *Logger) LogBan(ctx context.Context, adminID, targetUserID, roomID string, duration int64) error {
	return al.Log(ctx, Event{
		UserID:       adminID,
		Action:       "user.ban",
		ResourceID:   targetUserID,
		ResourceType: "user",
		Metadata: map[string]interface{}{
			"room_id":  roomID,
			"duration": duration,
		},
	})
}
