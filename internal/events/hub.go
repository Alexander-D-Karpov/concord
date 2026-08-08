package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Hub is the single in-process pub/sub fan-out point for server events. It
// tracks connected clients and their room subscriptions and delivers events to
// each client's buffered SendChan on a best-effort basis. All state is guarded
// by mu; once shutdown is set, AddClient rejects new clients.
type Hub struct {
	mu       sync.RWMutex
	clients  map[string]*Client
	rooms    map[string]map[string]bool
	logger   *zap.Logger
	pool     *pgxpool.Pool
	asides   *cache.AsidePattern
	shutdown bool
}

// Client is one connected event-stream subscriber. Events are queued on the
// buffered SendChan and delivered by a dedicated writePump goroutine; ctx/cancel
// tie the client's lifetime to its gRPC stream. RoomSubs is guarded by the
// client's own mu.
type Client struct {
	UserID   string
	Stream   streamv1.StreamService_EventStreamServer
	RoomSubs map[string]bool
	SendChan chan *streamv1.ServerEvent
	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewHub creates an empty Hub. The pool is used to look up a user's room and DM
// memberships for auto-subscription, and asides provides a cache-aside layer in
// front of those lookups (it may be nil to always hit the database).
func NewHub(logger *zap.Logger, pool *pgxpool.Pool, asides *cache.AsidePattern) *Hub {
	return &Hub{
		clients:  make(map[string]*Client),
		rooms:    make(map[string]map[string]bool),
		logger:   logger,
		pool:     pool,
		asides:   asides,
		shutdown: false,
	}
}

// Logger returns the hub's logger so callers sharing the hub can log with the
// same instance.
func (h *Hub) Logger() *zap.Logger {
	return h.logger
}

// AddClient registers a new client for userID, replacing any existing entry,
// and starts its writePump goroutine. It returns nil if the hub is shutting
// down. Before returning it auto-subscribes the user to their rooms and DM
// channels, waiting up to 5s for that to finish synchronously and otherwise
// letting it continue in the background.
func (h *Hub) AddClient(userID string, stream streamv1.StreamService_EventStreamServer) *Client {
	h.mu.Lock()
	if h.shutdown {
		h.mu.Unlock()
		h.logger.Warn("rejecting new client during shutdown", zap.String("user_id", userID))
		return nil
	}

	ctx, cancel := context.WithCancel(stream.Context())

	client := &Client{
		UserID:   userID,
		Stream:   stream,
		RoomSubs: make(map[string]bool),
		SendChan: make(chan *streamv1.ServerEvent, 500),
		ctx:      ctx,
		cancel:   cancel,
	}

	h.clients[userID] = client
	h.logger.Info("client connected", zap.String("user_id", userID))

	go client.writePump(h.logger)

	h.mu.Unlock()

	userUUID, err := uuid.Parse(userID)
	if err == nil {
		subscriptionCtx, subscriptionCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer subscriptionCancel()

		done := make(chan struct{})
		go func() {
			h.autoSubscribeUserRooms(subscriptionCtx, client, userUUID)
			close(done)
		}()

		select {
		case <-done:
			h.logger.Info("auto-subscription completed synchronously", zap.String("user_id", userID))
		case <-subscriptionCtx.Done():
			h.logger.Warn("auto-subscription timeout, continuing in background", zap.String("user_id", userID))
		}
	} else {
		h.logger.Error("failed to parse user ID for auto-subscribe", zap.String("user_id", userID), zap.Error(err))
	}

	return client
}

// autoSubscribeUserRooms subscribes client to every room the user belongs to
// (via the cache-aside layer when available, else a direct DB query) plus their
// DM channels. It stops early if ctx is cancelled, subscribing to as many rooms
// as it reached before then.
func (h *Hub) autoSubscribeUserRooms(ctx context.Context, client *Client, userID uuid.UUID) {
	const ttl = 30 * time.Second
	key := fmt.Sprintf("u:%s:rooms", userID.String())

	h.logger.Info("starting auto-subscribe for user", zap.String("user_id", userID.String()))

	var roomIDs []string
	load := func() (interface{}, error) {
		rows, err := h.pool.Query(ctx, `SELECT room_id FROM memberships WHERE user_id = $1`, userID)
		if err != nil {
			h.logger.Error("failed to query memberships", zap.String("user_id", userID.String()), zap.Error(err))
			return nil, err
		}
		defer rows.Close()

		var ids []string
		for rows.Next() {
			var rid uuid.UUID
			if err := rows.Scan(&rid); err != nil {
				h.logger.Warn("failed to scan room_id", zap.Error(err))
				continue
			}
			ids = append(ids, rid.String())
		}
		h.logger.Info("loaded room IDs from DB", zap.String("user_id", userID.String()), zap.Int("count", len(ids)))
		return ids, nil
	}

	if h.asides != nil {
		v, err := h.asides.GetOrLoad(ctx, key, ttl, load)
		if err == nil {
			switch t := v.(type) {
			case []string:
				roomIDs = t
			case []interface{}:
				for _, x := range t {
					if s, ok := x.(string); ok {
						roomIDs = append(roomIDs, s)
					}
				}
			default:
				b, _ := json.Marshal(v)
				_ = json.Unmarshal(b, &roomIDs)
			}
		} else {
			h.logger.Warn("GetOrLoad rooms failed; falling back to DB", zap.Error(err))
		}
	}

	if roomIDs == nil {
		rows, err := h.pool.Query(ctx, `SELECT room_id FROM memberships WHERE user_id = $1`, userID)
		if err != nil {
			h.logger.Error("failed to query user rooms", zap.String("user_id", userID.String()), zap.Error(err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var rid uuid.UUID
			if err := rows.Scan(&rid); err != nil {
				continue
			}
			roomIDs = append(roomIDs, rid.String())
		}
	}

	// Also subscribe to DM channels
	dmRows, err := h.pool.Query(ctx,
		`SELECT id FROM dm_channels WHERE user1_id = $1 OR user2_id = $1`, userID)
	if err != nil {
		h.logger.Warn("failed to query DM channels", zap.String("user_id", userID.String()), zap.Error(err))
	} else {
		for dmRows.Next() {
			var cid uuid.UUID
			if err := dmRows.Scan(&cid); err != nil {
				continue
			}
			roomIDs = append(roomIDs, cid.String())
		}
		dmRows.Close()
	}

	if len(roomIDs) == 0 {
		h.logger.Info("no rooms/channels found for user", zap.String("user_id", userID.String()))
		return
	}

	successCount := 0
	for _, rid := range roomIDs {
		select {
		case <-ctx.Done():
			h.logger.Warn("auto-subscribe context cancelled",
				zap.String("user_id", client.UserID),
				zap.Int("subscribed", successCount),
				zap.Int("total", len(roomIDs)),
			)
			return
		default:
		}

		if h.Subscribe(client.UserID, rid) {
			successCount++
		}
	}

	h.logger.Info("auto-subscribe completed",
		zap.String("user_id", client.UserID),
		zap.Int("total", len(roomIDs)),
		zap.Int("successful", successCount),
	)
}

// BroadcastToRoom delivers event to every client subscribed to roomID. It fills
// in a generated EventId and CreatedAt if unset. Delivery is best-effort: a
// client whose SendChan is full has the event dropped (logged) rather than
// blocking the broadcast. Rooms with no subscribers are skipped cheaply.
func (h *Hub) BroadcastToRoom(roomID string, event *streamv1.ServerEvent) {
	h.mu.RLock()
	users, exists := h.rooms[roomID]
	if !exists {
		h.mu.RUnlock()
		h.logger.Debug("no subscribers in room, skipping broadcast",
			zap.String("room_id", roomID),
			zap.String("event_id", event.GetEventId()),
			zap.String("event_type", fmt.Sprintf("%T", event.Payload)),
		)
		return
	}

	userIDs := make([]string, 0, len(users))
	for userID := range users {
		userIDs = append(userIDs, userID)
	}
	h.mu.RUnlock()

	if len(userIDs) == 0 {
		h.logger.Debug("room has zero subscribers, skipping broadcast",
			zap.String("room_id", roomID),
			zap.String("event_id", event.GetEventId()),
			zap.String("event_type", fmt.Sprintf("%T", event.Payload)),
		)
		return
	}

	if event.EventId == "" {
		event.EventId = uuid.New().String()
	}
	if event.CreatedAt == nil {
		event.CreatedAt = timestamppb.Now()
	}

	h.logger.Info("broadcasting event to room",
		zap.String("room_id", roomID),
		zap.Int("subscribers", len(userIDs)),
		zap.String("event_id", event.EventId),
		zap.String("event_type", fmt.Sprintf("%T", event.Payload)),
	)

	successCount := 0
	for _, userID := range userIDs {
		h.mu.RLock()
		client, ok := h.clients[userID]
		h.mu.RUnlock()

		if ok {
			select {
			case client.SendChan <- event:
				successCount++
				h.logger.Debug("sent event to user",
					zap.String("user_id", userID),
					zap.String("event_id", event.EventId),
				)
			default:
				h.logger.Warn("client channel full, dropping event",
					zap.String("user_id", userID),
					zap.String("event_id", event.EventId),
				)
			}
		} else {
			h.logger.Warn("client not found during broadcast",
				zap.String("user_id", userID),
				zap.String("room_id", roomID),
			)
		}
	}

	h.logger.Info("broadcast completed",
		zap.String("room_id", roomID),
		zap.String("event_id", event.EventId),
		zap.Int("total_subscribers", len(userIDs)),
		zap.Int("successful_sends", successCount),
	)
}

// RemoveClient disconnects the client for userID: it removes them from all
// subscribed rooms (deleting now-empty rooms), cancels the client context, and
// closes its SendChan so writePump exits. It is a no-op if no such client
// exists.
func (h *Hub) RemoveClient(userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	client, ok := h.clients[userID]
	if !ok {
		return
	}

	for roomID := range client.RoomSubs {
		if users, exists := h.rooms[roomID]; exists {
			delete(users, userID)
			if len(users) == 0 {
				delete(h.rooms, roomID)
			}
		}
	}

	client.cancel()
	close(client.SendChan)
	delete(h.clients, userID)

	h.logger.Info("client disconnected", zap.String("user_id", userID))
}

// Subscribe adds the userID's client to roomID and records the room in the
// client's RoomSubs. It returns false if the client is not connected or its
// context is already cancelled.
func (h *Hub) Subscribe(userID, roomID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	client, ok := h.clients[userID]
	if !ok {
		h.logger.Warn("attempted to subscribe non-existent client",
			zap.String("user_id", userID),
			zap.String("room_id", roomID),
		)
		return false
	}

	select {
	case <-client.ctx.Done():
		h.logger.Warn("attempted to subscribe cancelled client",
			zap.String("user_id", userID),
			zap.String("room_id", roomID),
		)
		return false
	default:
	}

	client.mu.Lock()
	client.RoomSubs[roomID] = true
	client.mu.Unlock()

	if _, exists := h.rooms[roomID]; !exists {
		h.rooms[roomID] = make(map[string]bool)
	}
	h.rooms[roomID][userID] = true

	h.logger.Debug("client subscribed to room",
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
	)

	return true
}

// Unsubscribe removes the userID's client from roomID, deleting the room entry
// when it becomes empty. It is a no-op if the client is not connected.
func (h *Hub) Unsubscribe(userID, roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	client, ok := h.clients[userID]
	if !ok {
		return
	}

	client.mu.Lock()
	delete(client.RoomSubs, roomID)
	client.mu.Unlock()

	if users, exists := h.rooms[roomID]; exists {
		delete(users, userID)
		if len(users) == 0 {
			delete(h.rooms, roomID)
		}
	}

	h.logger.Debug("client unsubscribed from room",
		zap.String("user_id", userID),
		zap.String("room_id", roomID),
	)
}

// NotifyRoomJoin subscribes a connected user to roomID asynchronously (in a
// goroutine) when the user joins a room. It does nothing if the user has no
// active client. Use NotifyRoomJoinSync when the caller needs the result.
func (h *Hub) NotifyRoomJoin(userID, roomID string) {
	h.mu.RLock()
	_, exists := h.clients[userID]
	h.mu.RUnlock()

	if exists {
		go func() {
			if h.Subscribe(userID, roomID) {
				h.logger.Info("user joined room, subscribed to stream",
					zap.String("user_id", userID),
					zap.String("room_id", roomID),
				)
			} else {
				h.logger.Warn("failed to subscribe user to room",
					zap.String("user_id", userID),
					zap.String("room_id", roomID),
				)
			}
		}()
	}
}

// NotifyRoomLeave unsubscribes a connected user from roomID when they leave.
// It does nothing if the user has no active client.
func (h *Hub) NotifyRoomLeave(userID, roomID string) {
	h.mu.RLock()
	_, exists := h.clients[userID]
	h.mu.RUnlock()

	if exists {
		h.Unsubscribe(userID, roomID)
		h.logger.Info("user left room, unsubscribed from stream",
			zap.String("user_id", userID),
			zap.String("room_id", roomID),
		)
	}
}

// BroadcastToUser delivers event directly to a single user's client, filling in
// EventId and CreatedAt if unset. Like room broadcasts it is best-effort: the
// event is dropped (logged) if the client is absent or its SendChan is full.
func (h *Hub) BroadcastToUser(userID string, event *streamv1.ServerEvent) {
	h.mu.RLock()
	client, ok := h.clients[userID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	if event.EventId == "" {
		event.EventId = uuid.New().String()
	}
	if event.CreatedAt == nil {
		event.CreatedAt = timestamppb.Now()
	}
	select {
	case client.SendChan <- event:
	default:
		h.logger.Warn("client channel full, dropping user event",
			zap.String("user_id", userID),
			zap.String("event_id", event.EventId),
		)
	}
}

// NotifyRoomJoinSync is the synchronous form of NotifyRoomJoin: it subscribes
// the connected user to roomID inline and returns whether the subscription
// succeeded, returning false if the user has no active client.
func (h *Hub) NotifyRoomJoinSync(userID, roomID string) bool {
	h.mu.RLock()
	_, exists := h.clients[userID]
	h.mu.RUnlock()

	if !exists {
		h.logger.Warn("cannot subscribe non-existent client",
			zap.String("user_id", userID),
			zap.String("room_id", roomID),
		)
		return false
	}

	success := h.Subscribe(userID, roomID)
	if success {
		h.logger.Info("user joined room, subscribed to stream",
			zap.String("user_id", userID),
			zap.String("room_id", roomID),
		)
	} else {
		h.logger.Warn("failed to subscribe user to room",
			zap.String("user_id", userID),
			zap.String("room_id", roomID),
		)
	}
	return success
}

// writePump is the client's single writer goroutine: it forwards events from
// SendChan to the gRPC stream and returns when SendChan is closed, the stream
// send fails, or the client context is cancelled. A 30s ticker wakes it
// periodically but currently performs no keepalive work.
func (c *Client) writePump(logger *zap.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-c.SendChan:
			if !ok {
				return
			}

			if err := c.Stream.Send(event); err != nil {
				logger.Debug("failed to send event",
					zap.String("user_id", c.UserID),
					zap.Error(err),
				)
				return
			}

		case <-ticker.C:
			continue

		case <-c.ctx.Done():
			logger.Debug("client context cancelled", zap.String("user_id", c.UserID))
			return
		}
	}
}

// Shutdown marks the hub as shutting down (so AddClient rejects new clients),
// then force-disconnects every current client by cancelling its context and
// closing its SendChan. It always returns nil; ctx is currently unused.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.shutdown = true

	clientsToClose := make([]*Client, 0, len(h.clients))
	for _, client := range h.clients {
		clientsToClose = append(clientsToClose, client)
	}

	h.clients = make(map[string]*Client)
	h.rooms = make(map[string]map[string]bool)
	h.mu.Unlock()

	h.logger.Info("forcing shutdown of event hub", zap.Int("clients", len(clientsToClose)))

	for _, client := range clientsToClose {
		client.cancel()

		select {
		case <-client.SendChan:
		default:
		}

		close(client.SendChan)
	}

	h.logger.Info("all clients force-disconnected")
	return nil
}

// RoomHasSubscribers reports whether roomID currently has at least one
// subscribed client, letting callers skip work for empty rooms.
func (h *Hub) RoomHasSubscribers(roomID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	subs, ok := h.rooms[roomID]
	return ok && len(subs) > 0
}
