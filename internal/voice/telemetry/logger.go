package telemetry

import (
	"go.uber.org/zap"
)

// Logger wraps a zap.Logger with typed helpers for voice data-plane events.
// Per-packet events log at Debug (off in production) while session lifecycle
// events log at Info; it holds no state and is safe for concurrent use.
type Logger struct {
	base *zap.Logger
}

// NewLogger wraps base with the voice event helpers. base must be non-nil.
func NewLogger(base *zap.Logger) *Logger {
	return &Logger{base: base}
}

// LogPacketReceived records an inbound media packet at Debug level; size is the
// wire payload length in bytes. High-volume — effectively a no-op unless Debug
// is enabled.
func (l *Logger) LogPacketReceived(userID, roomID string, size int) {
	l.base.Debug("packet received",
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
		zap.Int("size", size),
	)
}

// LogPacketSent records an outbound media packet at Debug level; size is the
// wire payload length in bytes. High-volume — a no-op unless Debug is enabled.
func (l *Logger) LogPacketSent(userID, roomID string, size int) {
	l.base.Debug("packet sent",
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
		zap.Int("size", size),
	)
}

// LogSessionCreated records a new voice session at Info level, keyed by the
// wire SSRC (sessionID) alongside the user and room.
func (l *Logger) LogSessionCreated(sessionID uint32, userID, roomID string) {
	l.base.Info("session created",
		zap.Uint32("session_id", sessionID),
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
	)
}

// LogSessionEnded records session teardown at Info level, mirroring
// LogSessionCreated so the pair brackets a session's lifetime in the logs.
func (l *Logger) LogSessionEnded(sessionID uint32, userID, roomID string) {
	l.base.Info("session ended",
		zap.Uint32("session_id", sessionID),
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
	)
}
