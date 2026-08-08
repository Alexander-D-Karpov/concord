package push

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	priorityHigh   = "high"
	priorityNormal = "normal"
	callTTL        = 30 * time.Second
	queueSize      = 1024
)

// job is one enqueued push for a user.
type job struct {
	userID      uuid.UUID
	data        map[string]string
	priority    string
	collapseKey string
	ttl         time.Duration
}

// Dispatcher enqueues pushes and delivers them asynchronously via a worker so the
// originating RPC never blocks on FCM. Invalid tokens reported by the Sender are
// pruned from the repository (self-healing).
type Dispatcher struct {
	repo   *Repository
	sender Sender
	logger *zap.Logger
	queue  chan job
}

// NewDispatcher builds a Dispatcher. Call Start to run the worker.
func NewDispatcher(repo *Repository, sender Sender, logger *zap.Logger) *Dispatcher {
	return &Dispatcher{repo: repo, sender: sender, logger: logger, queue: make(chan job, queueSize)}
}

// Start runs the delivery worker until ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-d.queue:
				d.deliver(ctx, j)
			}
		}
	}()
}

// enqueue adds a job without blocking; if the queue is full the push is dropped and
// logged (push is best-effort, never backpressure on the caller).
func (d *Dispatcher) enqueue(j job) {
	select {
	case d.queue <- j:
	default:
		d.logger.Warn("push queue full, dropping notification", zap.String("user_id", j.userID.String()))
	}
}

// DispatchChat enqueues a chat/DM/mention push for userID.
func (d *Dispatcher) DispatchChat(userID uuid.UUID, conversationID, messageID, senderID string) {
	d.enqueue(job{userID: userID, data: buildChatData(conversationID, messageID, senderID), priority: priorityNormal, collapseKey: conversationID})
}

// DispatchCall enqueues a high-priority incoming-call ring for userID.
func (d *Dispatcher) DispatchCall(userID uuid.UUID, callID, roomOrDMID, callerID string) {
	d.enqueue(job{userID: userID, data: buildCallData(callID, roomOrDMID, callerID), priority: priorityHigh, ttl: callTTL})
}

// deliver looks up the user's devices, sends one message per token, and prunes any
// tokens the provider reported invalid.
func (d *Dispatcher) deliver(ctx context.Context, j job) {
	devices, err := d.repo.ListByUser(ctx, j.userID)
	if err != nil {
		d.logger.Warn("push device lookup failed", zap.Error(err))
		return
	}
	if len(devices) == 0 {
		return
	}
	msgs := make([]Message, 0, len(devices))
	for _, dev := range devices {
		msgs = append(msgs, Message{Token: dev.FCMToken, Data: j.data, Priority: j.priority, CollapseKey: j.collapseKey, TTL: j.ttl})
	}
	invalid, err := d.sender.Send(ctx, msgs)
	if err != nil {
		d.logger.Warn("push send failed", zap.Error(err))
	}
	for _, tok := range invalid {
		if err := d.repo.DeleteByToken(ctx, tok); err != nil {
			d.logger.Warn("failed to prune invalid push token", zap.Error(err))
		}
	}
}
