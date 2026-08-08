package push

import (
	"context"
	"time"
)

// Message is a transport-agnostic push: a single device token plus data payload and
// delivery hints. The Sender adapter maps it to the concrete provider message.
type Message struct {
	Token       string
	Data        map[string]string
	Priority    string // "high" | "normal"
	CollapseKey string
	TTL         time.Duration
}

// Sender delivers messages to a push provider, returning tokens the provider reports
// as permanently invalid (unregistered) so the caller can prune them.
type Sender interface {
	Send(ctx context.Context, msgs []Message) (invalidTokens []string, err error)
}

// buildChatData is the FCM data payload for a chat/DM/mention notification.
func buildChatData(conversationID, messageID, senderID string) map[string]string {
	return map[string]string{
		"type":            "message",
		"conversation_id": conversationID,
		"message_id":      messageID,
		"sender_id":       senderID,
	}
}

// buildCallData is the FCM data payload for an incoming-call ring.
func buildCallData(callID, roomOrDMID, callerID string) map[string]string {
	return map[string]string{
		"type":          "call_invite",
		"call_id":       callID,
		"room_or_dm_id": roomOrDMID,
		"caller_id":     callerID,
	}
}
