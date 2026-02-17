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

type Hub struct {
	mu       sync.RWMutex
	clients  map[string]*Client
	rooms    map[string]map[string]bool
	logger   *zap.Logger
	pool     *pgxpool.Pool
	asides   *cache.AsidePattern
	shutdown bool
}

type Client struct {
	UserID   string
	Stream   streamv1.StreamService_EventStreamServer
	RoomSubs map[string]bool
	SendChan chan *streamv1.ServerEvent
	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
}

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

func (h *Hub) Logger() *zap.Logger {
	return h.logger
}

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
			h.logger.Info("loaded room IDs from cache", zap.String("user_id", userID.String()), zap.Int("count", len(roomIDs)))
		} else {
			h.logger.Warn("GetOrLoad rooms failed; falling back to DB", zap.Error(err))
		}
	}

	if roomIDs == nil {
		h.logger.Info("cache disabled or failed, querying DB directly", zap.String("user_id", userID.String()))
		rows, err := h.pool.Query(ctx, `SELECT room_id FROM memberships WHERE user_id = $1`, userID)
		if err != nil {
			h.logger.Error("failed to query user rooms", zap.String("user_id", userID.String()), zap.Error(err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var rid uuid.UUID
			if err := rows.Scan(&rid); err != nil {
				h.logger.Warn("failed to scan room_id", zap.Error(err))
				continue
			}
			roomIDs = append(roomIDs, rid.String())
		}
		h.logger.Info("loaded room IDs from DB (fallback)", zap.String("user_id", userID.String()), zap.Int("count", len(roomIDs)))
	}

	if len(roomIDs) == 0 {
		h.logger.Info("no rooms found for user", zap.String("user_id", userID.String()))
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
			h.logger.Debug("auto-subscribed to room",
				zap.String("user_id", client.UserID),
				zap.String("room_id", rid),
			)
		} else {
			h.logger.Warn("failed to auto-subscribe to room",
				zap.String("user_id", client.UserID),
				zap.String("room_id", rid),
			)
		}
	}

	h.logger.Info("auto-subscribe completed",
		zap.String("user_id", client.UserID),
		zap.Int("total_rooms", len(roomIDs)),
		zap.Int("successful", successCount),
	)
}

func (h *Hub) BroadcastToRoom(roomID string, event *streamv1.ServerEvent) {
	h.mu.RLock()
	users, exists := h.rooms[roomID]
	if !exists {
		h.mu.RUnlock()
		h.logger.Error("attempted to broadcast to room with no subscribers",
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
		h.logger.Error("room has zero subscribers",
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

func (h *Hub) RoomHasSubscribers(roomID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	subs, ok := h.rooms[roomID]
	return ok && len(subs) > 0
}
