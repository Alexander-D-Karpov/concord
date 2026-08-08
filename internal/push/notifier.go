package push

import (
	"context"
	"strconv"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Notifier applies the per-user mute policy and then enqueues pushes. It is what the
// chat and DM services call after a message is created.
type Notifier struct {
	mute   MuteChecker
	disp   *Dispatcher
	logger *zap.Logger
}

// NewNotifier builds a Notifier.
func NewNotifier(mute MuteChecker, disp *Dispatcher, logger *zap.Logger) *Notifier {
	return &Notifier{mute: mute, disp: disp, logger: logger}
}

// PushRoomMessage pushes a room message to userID unless they muted the room.
func (n *Notifier) PushRoomMessage(ctx context.Context, userID, roomID uuid.UUID, messageID int64, senderID uuid.UUID) {
	if muted, err := n.mute.IsMuted(ctx, userID, &roomID, nil); err != nil {
		n.logger.Warn("mute check failed", zap.Error(err))
	} else if muted {
		return
	}
	n.disp.DispatchChat(userID, roomID.String(), strconv.FormatInt(messageID, 10), senderID.String())
}

// PushDMMessage pushes a DM to userID unless they muted the channel.
func (n *Notifier) PushDMMessage(ctx context.Context, userID, channelID uuid.UUID, messageID int64, senderID uuid.UUID) {
	if muted, err := n.mute.IsMuted(ctx, userID, nil, &channelID); err != nil {
		n.logger.Warn("mute check failed", zap.Error(err))
	} else if muted {
		return
	}
	n.disp.DispatchChat(userID, channelID.String(), strconv.FormatInt(messageID, 10), senderID.String())
}

// PushCall enqueues a high-priority incoming-call ring.
func (n *Notifier) PushCall(userID uuid.UUID, callID, roomOrDMID, callerID string) {
	n.disp.DispatchCall(userID, callID, roomOrDMID, callerID)
}
