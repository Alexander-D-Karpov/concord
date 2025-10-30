package events

import (
	"context"
	"sync"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Hub struct {
	mu      sync.RWMutex
	clients map[string]*Client
	rooms   map[string]map[string]bool
	logger  *zap.Logger
	pool    *pgxpool.Pool
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

func NewHub(logger *zap.Logger, pool *pgxpool.Pool) *Hub {
	return &Hub{
		clients: make(map[string]*Client),
		rooms:   make(map[string]map[string]bool),
		logger:  logger,
		pool:    pool,
	}
}

func (h *Hub) Logger() *zap.Logger {
	return h.logger
}

func (h *Hub) AddClient(userID string, stream streamv1.StreamService_EventStreamServer) *Client {
	ctx, cancel := context.WithCancel(stream.Context())

	client := &Client{
		UserID:   userID,
		Stream:   stream,
		RoomSubs: make(map[string]bool),
		SendChan: make(chan *streamv1.ServerEvent, 100),
		ctx:      ctx,
		cancel:   cancel,
	}

	h.mu.Lock()
	h.clients[userID] = client
	h.mu.Unlock()

	h.logger.Info("client connected", zap.String("user_id", userID))

	go client.writePump(h.logger)

	userUUID, err := uuid.Parse(userID)
	if err == nil {
		h.autoSubscribeUserRooms(ctx, client, userUUID)
	}

	return client
}

func (h *Hub) autoSubscribeUserRooms(ctx context.Context, client *Client, userID uuid.UUID) {
	query := `
		SELECT room_id FROM memberships WHERE user_id = $1
	`

	rows, err := h.pool.Query(ctx, query, userID)
	if err != nil {
		h.logger.Error("failed to query user rooms", zap.Error(err))
		return
	}
	defer rows.Close()

	for rows.Next() {
		var roomID uuid.UUID
		if err := rows.Scan(&roomID); err != nil {
			continue
		}

		roomIDStr := roomID.String()
		h.Subscribe(client.UserID, roomIDStr)
		h.logger.Debug("auto-subscribed to room",
			zap.String("user_id", client.UserID),
			zap.String("room_id", roomIDStr),
		)
	}
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

func (h *Hub) Subscribe(userID, roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	client, ok := h.clients[userID]
	if !ok {
		return
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
		h.Subscribe(userID, roomID)
		h.logger.Info("user joined room, subscribed to stream",
			zap.String("user_id", userID),
			zap.String("room_id", roomID),
		)
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

func (h *Hub) BroadcastToRoom(roomID string, event *streamv1.ServerEvent) {
	h.mu.RLock()
	users, exists := h.rooms[roomID]
	if !exists {
		h.mu.RUnlock()
		return
	}

	userIDs := make([]string, 0, len(users))
	for userID := range users {
		userIDs = append(userIDs, userID)
	}
	h.mu.RUnlock()

	if event.EventId == "" {
		event.EventId = uuid.New().String()
	}
	if event.CreatedAt == nil {
		event.CreatedAt = timestamppb.Now()
	}

	for _, userID := range userIDs {
		h.mu.RLock()
		client, ok := h.clients[userID]
		h.mu.RUnlock()

		if ok {
			select {
			case client.SendChan <- event:
			case <-time.After(100 * time.Millisecond):
				h.logger.Warn("failed to send event to client",
					zap.String("user_id", userID),
					zap.String("event_id", event.EventId),
				)
			}
		}
	}
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
				logger.Error("failed to send event",
					zap.String("user_id", c.UserID),
					zap.Error(err),
				)
				return
			}

		case <-ticker.C:

		case <-c.ctx.Done():
			return
		}
	}
}
