package chat

import (
	"context"
	"strconv"

	chatv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/chat/v1"
	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	chatv1.UnimplementedChatServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

func (h *Handler) SendMessage(ctx context.Context, req *chatv1.SendMessageRequest) (*chatv1.SendMessageResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}
	if req.Content == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("content is required"))
	}

	msg, err := h.service.SendMessage(ctx, req.RoomId, req.Content)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.SendMessageResponse{
		Message: &commonv1.Message{
			Id:        strconv.FormatInt(msg.ID, 10),
			RoomId:    msg.RoomID.String(),
			AuthorId:  msg.AuthorID.String(),
			Content:   msg.Content,
			CreatedAt: timestamppb.New(msg.CreatedAt),
		},
	}, nil
}

func (h *Handler) EditMessage(ctx context.Context, req *chatv1.EditMessageRequest) (*commonv1.Message, error) {
	if req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("message_id is required"))
	}
	if req.Content == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("content is required"))
	}

	msg, err := h.service.EditMessage(ctx, req.MessageId, req.Content)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoMsg := &commonv1.Message{
		Id:        strconv.FormatInt(msg.ID, 10),
		RoomId:    msg.RoomID.String(),
		AuthorId:  msg.AuthorID.String(),
		Content:   msg.Content,
		CreatedAt: timestamppb.New(msg.CreatedAt),
	}

	if msg.EditedAt != nil {
		protoMsg.EditedAt = timestamppb.New(*msg.EditedAt)
	}

	return protoMsg, nil
}

func (h *Handler) DeleteMessage(ctx context.Context, req *chatv1.DeleteMessageRequest) (*chatv1.EmptyResponse, error) {
	if req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("message_id is required"))
	}

	if err := h.service.DeleteMessage(ctx, req.MessageId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.EmptyResponse{}, nil
}

func (h *Handler) ListMessages(ctx context.Context, req *chatv1.ListMessagesRequest) (*chatv1.ListMessagesResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}

	var beforeID, afterID *string
	if req.BeforeId != "" {
		beforeID = &req.BeforeId
	}
	if req.AfterId != "" {
		afterID = &req.AfterId
	}

	messages, err := h.service.ListMessages(ctx, req.RoomId, beforeID, afterID, limit)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoMessages := make([]*commonv1.Message, len(messages))
	for i, msg := range messages {
		protoMsg := &commonv1.Message{
			Id:        strconv.FormatInt(msg.ID, 10),
			RoomId:    msg.RoomID.String(),
			AuthorId:  msg.AuthorID.String(),
			Content:   msg.Content,
			CreatedAt: timestamppb.New(msg.CreatedAt),
		}
		if msg.EditedAt != nil {
			protoMsg.EditedAt = timestamppb.New(*msg.EditedAt)
		}
		if msg.DeletedAt != nil {
			protoMsg.Deleted = true
		}
		protoMessages[i] = protoMsg
	}

	return &chatv1.ListMessagesResponse{
		Messages: protoMessages,
		HasMore:  len(messages) == limit,
	}, nil
}

func (h *Handler) SearchMessages(ctx context.Context, req *chatv1.SearchMessagesRequest) (*chatv1.SearchMessagesResponse, error) {
	return &chatv1.SearchMessagesResponse{
		Messages: []*commonv1.Message{},
	}, nil
}
