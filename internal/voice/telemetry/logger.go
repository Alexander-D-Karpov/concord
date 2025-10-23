package telemetry

import (
	"go.uber.org/zap"
)

type Logger struct {
	base *zap.Logger
}

func NewLogger(base *zap.Logger) *Logger {
	return &Logger{base: base}
}

func (l *Logger) LogPacketReceived(userID, roomID string, size int) {
	l.base.Debug("packet received",
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
		zap.Int("size", size),
	)
}

func (l *Logger) LogPacketSent(userID, roomID string, size int) {
	l.base.Debug("packet sent",
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
		zap.Int("size", size),
	)
}

func (l *Logger) LogSessionCreated(sessionID uint32, userID, roomID string) {
	l.base.Info("session created",
		zap.Uint32("session_id", sessionID),
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
	)
}

func (l *Logger) LogSessionEnded(sessionID uint32, userID, roomID string) {
	l.base.Info("session ended",
		zap.Uint32("session_id", sessionID),
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
	)
}
