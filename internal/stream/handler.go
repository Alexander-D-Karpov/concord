package stream

import (
	"context"
	"io"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Handler struct {
	streamv1.UnimplementedStreamServiceServer
	hub      *events.Hub
	presence *users.PresenceManager
}

func NewHandler(hub *events.Hub, presence *users.PresenceManager) *Handler {
	return &Handler{
		hub:      hub,
		presence: presence,
	}
}

func (h *Handler) EventStream(stream streamv1.StreamService_EventStreamServer) error {
	ctx := stream.Context()
	logger := logging.FromContext(ctx)

	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	if err := h.presence.Heartbeat(ctx, userUUID); err != nil {
		logger.Warn("failed to set user online", zap.Error(err))
	}

	logger.Info("adding client to hub", zap.String("user_id", userID))
	client := h.hub.AddClient(userID, stream)
	if client == nil {
		return errors.ToGRPCError(errors.Internal("failed to add client", nil))
	}

	defer func() {
		logger.Info("removing client from hub", zap.String("user_id", userID))
		h.hub.RemoveClient(userID)

		dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.presence.SetOffline(dbCtx, userUUID); err != nil {
			logger.Warn("failed to set user offline",
				zap.String("user_id", userID),
				zap.Error(err),
			)
		}
	}()

	logger.Info("stream connection established", zap.String("user_id", userID))

	errChan := make(chan error, 1)
	go func() {
		for {
			clientEvent, err := stream.Recv()
			if err == io.EOF {
				logger.Info("client closed stream", zap.String("user_id", userID))
				errChan <- nil
				return
			}
			if err != nil {
				st, ok := status.FromError(err)
				if ok && (st.Code() == codes.Canceled || st.Code() == codes.DeadlineExceeded) {
					logger.Info("stream recv cancelled",
						zap.String("user_id", userID),
						zap.String("reason", st.Message()),
					)
					return
				}
				logger.Error("stream recv error",
					zap.String("user_id", userID),
					zap.Error(err),
				)
				errChan <- err
				return
			}

			if _, ok := clientEvent.Payload.(*streamv1.ClientEvent_Ack); ok {
				if err := h.presence.Heartbeat(ctx, userUUID); err != nil {
					logger.Debug("heartbeat failed", zap.Error(err))
				}
			}

			h.handleClientEvent(userID, clientEvent, logger)
		}
	}()

	select {
	case err := <-errChan:
		if err != nil && err != io.EOF {
			logger.Error("stream error", zap.Error(err))
		}
		return err
	case <-ctx.Done():
		logger.Info("stream context cancelled", zap.String("user_id", userID))
		return ctx.Err()
	}
}

func (h *Handler) handleClientEvent(userID string, event *streamv1.ClientEvent, logger *zap.Logger) {
	switch payload := event.Payload.(type) {
	case *streamv1.ClientEvent_Ack:
		logger.Debug("received ack",
			zap.String("user_id", userID),
			zap.String("event_id", payload.Ack.EventId),
		)
	}
}
