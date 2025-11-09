package chat

import (
	"context"
	"strconv"

	chatv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/chat/v1"
	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/storage"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	chatv1.UnimplementedChatServiceServer
	service *Service
	storage *storage.Storage
}

func NewHandler(service *Service, storageService *storage.Storage) *Handler {
	return &Handler{
		service: service,
		storage: storageService,
	}
}

func (h *Handler) SendMessage(ctx context.Context, req *chatv1.SendMessageRequest) (*chatv1.SendMessageResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}
	if req.Content == "" && len(req.Attachments) == 0 {
		return nil, errors.ToGRPCError(errors.BadRequest("content or attachments are required"))
	}

	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	var attachments []Attachment
	for _, upload := range req.Attachments {
		if len(upload.Data) == 0 {
			continue
		}

		fileInfo, err := h.storage.Store(ctx, upload.Data, upload.Filename, upload.ContentType)
		if err != nil {
			return nil, errors.ToGRPCError(errors.Internal("failed to store attachment", err))
		}

		attachment := Attachment{
			ID:          uuid.New(),
			URL:         fileInfo.URL,
			Filename:    fileInfo.Filename,
			ContentType: fileInfo.ContentType,
			Size:        fileInfo.Size,
			Width:       int(upload.Width),
			Height:      int(upload.Height),
		}
		attachments = append(attachments, attachment)
	}

	msg, err := h.service.SendMessage(ctx, req.RoomId, req.Content, req.ReplyToId, attachments)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.SendMessageResponse{
		Message: messageToProto(msg),
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

	return messageToProto(msg), nil
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
		protoMessages[i] = messageToProto(msg)
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

func (h *Handler) AddReaction(ctx context.Context, req *chatv1.AddReactionRequest) (*chatv1.EmptyResponse, error) {
	if req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("message_id is required"))
	}
	if req.Emoji == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("emoji is required"))
	}

	if err := h.service.AddReaction(ctx, req.MessageId, req.Emoji); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.EmptyResponse{}, nil
}

func (h *Handler) RemoveReaction(ctx context.Context, req *chatv1.RemoveReactionRequest) (*chatv1.EmptyResponse, error) {
	if req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("message_id is required"))
	}
	if req.Emoji == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("emoji is required"))
	}

	if err := h.service.RemoveReaction(ctx, req.MessageId, req.Emoji); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.EmptyResponse{}, nil
}

func (h *Handler) PinMessage(ctx context.Context, req *chatv1.PinMessageRequest) (*chatv1.EmptyResponse, error) {
	if req.RoomId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and message_id are required"))
	}

	if err := h.service.PinMessage(ctx, req.RoomId, req.MessageId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.EmptyResponse{}, nil
}

func (h *Handler) UnpinMessage(ctx context.Context, req *chatv1.UnpinMessageRequest) (*chatv1.EmptyResponse, error) {
	if req.RoomId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and message_id are required"))
	}

	if err := h.service.UnpinMessage(ctx, req.RoomId, req.MessageId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.EmptyResponse{}, nil
}

func (h *Handler) ListPinnedMessages(ctx context.Context, req *chatv1.ListPinnedMessagesRequest) (*chatv1.ListPinnedMessagesResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	messages, err := h.service.ListPinnedMessages(ctx, req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoMessages := make([]*commonv1.Message, len(messages))
	for i, msg := range messages {
		protoMessages[i] = messageToProto(msg)
	}

	return &chatv1.ListPinnedMessagesResponse{
		Messages: protoMessages,
	}, nil
}

func (h *Handler) GetThread(ctx context.Context, req *chatv1.GetThreadRequest) (*chatv1.GetThreadResponse, error) {
	if req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("message_id is required"))
	}

	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}

	messages, nextCursor, err := h.service.GetThread(ctx, req.MessageId, limit, req.Cursor)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoMessages := make([]*commonv1.Message, len(messages))
	for i, msg := range messages {
		protoMessages[i] = messageToProto(msg)
	}

	hasMore := nextCursor != ""

	return &chatv1.GetThreadResponse{
		Messages:   protoMessages,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

func messageToProto(msg *Message) *commonv1.Message {
	protoMsg := &commonv1.Message{
		Id:        strconv.FormatInt(msg.ID, 10),
		RoomId:    msg.RoomID.String(),
		AuthorId:  msg.AuthorID.String(),
		Content:   msg.Content,
		CreatedAt: timestamppb.New(msg.CreatedAt),
		Pinned:    msg.Pinned,
	}

	if msg.EditedAt != nil {
		protoMsg.EditedAt = timestamppb.New(*msg.EditedAt)
	}

	if msg.DeletedAt != nil {
		protoMsg.Deleted = true
	}

	if msg.ReplyToID != nil {
		protoMsg.ReplyToId = strconv.FormatInt(*msg.ReplyToID, 10)
	}

	protoMsg.ReplyCount = msg.ReplyCount

	reactions := make([]*commonv1.MessageReaction, len(msg.Reactions))
	for i, r := range msg.Reactions {
		reactions[i] = &commonv1.MessageReaction{
			Id:        r.ID.String(),
			MessageId: strconv.FormatInt(msg.ID, 10),
			UserId:    r.UserID.String(),
			Emoji:     r.Emoji,
			CreatedAt: timestamppb.New(r.CreatedAt),
		}
	}
	protoMsg.Reactions = reactions

	attachments := make([]*commonv1.MessageAttachment, len(msg.Attachments))
	for i, att := range msg.Attachments {
		attachments[i] = &commonv1.MessageAttachment{
			Id:          att.ID.String(),
			Url:         att.URL,
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Size:        att.Size,
			Width:       int32(att.Width),
			Height:      int32(att.Height),
			CreatedAt:   timestamppb.New(att.CreatedAt),
		}
	}
	protoMsg.Attachments = attachments

	return protoMsg
}
