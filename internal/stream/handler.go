package stream

import (
	"io"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"go.uber.org/zap"
)

type Handler struct {
	streamv1.UnimplementedStreamServiceServer
	hub *events.Hub
}

func NewHandler(hub *events.Hub) *Handler {
	return &Handler{
		hub: hub,
	}
}

func (h *Handler) EventStream(stream streamv1.StreamService_EventStreamServer) error {
	ctx := stream.Context()
	logger := logging.FromContext(ctx)

	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	h.hub.AddClient(userID, stream)
	defer h.hub.RemoveClient(userID)

	logger.Info("stream connection established", zap.String("user_id", userID))

	errChan := make(chan error, 1)

	go func() {
		for {
			clientEvent, err := stream.Recv()
			if err == io.EOF {
				errChan <- nil
				return
			}
			if err != nil {
				errChan <- err
				return
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
		logger.Info("stream connection closed", zap.String("user_id", userID))
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
