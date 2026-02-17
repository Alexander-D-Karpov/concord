package chat

import (
	"context"
	"strconv"

	chatv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/chat/v1"
	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/readtracking"
	"github.com/Alexander-D-Karpov/concord/internal/storage"
	"github.com/Alexander-D-Karpov/concord/internal/typing"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	chatv1.UnimplementedChatServiceServer
	service         *Service
	storage         *storage.Storage
	readTrackingSvc *readtracking.Service
	typingSvc       *typing.Service
}

func NewHandler(service *Service, storage *storage.Storage, readTrackingSvc *readtracking.Service, typingSvc *typing.Service) *Handler {
	return &Handler{
		service:         service,
		storage:         storage,
		readTrackingSvc: readTrackingSvc,
		typingSvc:       typingSvc,
	}
}

func (h *Handler) SendMessage(ctx context.Context, req *chatv1.SendMessageRequest) (*chatv1.SendMessageResponse, error) {
	if req.RoomId == "" || req.Content == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and content are required"))
	}

	var replyToID *int64
	if req.ReplyToId != "" {
		id, err := strconv.ParseInt(req.ReplyToId, 10, 64)
		if err != nil {
			return nil, errors.ToGRPCError(errors.BadRequest("invalid reply_to_id"))
		}
		replyToID = &id
	}

	var mentionIDs []uuid.UUID
	for _, id := range req.MentionUserIds {
		uid, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		mentionIDs = append(mentionIDs, uid)
	}

	msg, err := h.service.SendMessage(ctx, req.RoomId, req.Content, replyToID, mentionIDs)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.SendMessageResponse{
		Message: toProtoMessage(msg),
	}, nil
}

func (h *Handler) EditMessage(ctx context.Context, req *chatv1.EditMessageRequest) (*chatv1.EditMessageResponse, error) {
	if req.RoomId == "" || req.MessageId == "" || req.Content == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id, message_id and content are required"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	msg, err := h.service.EditMessage(ctx, req.RoomId, messageID, req.Content)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.EditMessageResponse{
		Message: toProtoMessage(msg),
	}, nil
}

func (h *Handler) DeleteMessage(ctx context.Context, req *chatv1.DeleteMessageRequest) (*chatv1.EmptyResponse, error) {
	if req.RoomId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and message_id are required"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	if err := h.service.DeleteMessage(ctx, req.RoomId, messageID); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.EmptyResponse{}, nil
}

func (h *Handler) ListMessages(ctx context.Context, req *chatv1.ListMessagesRequest) (*chatv1.ListMessagesResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	userID := interceptor.GetUserID(ctx)
	userUUID, _ := uuid.Parse(userID)
	roomUUID, _ := uuid.Parse(req.RoomId)

	limit := int(req.Limit)
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var beforeID, afterID *int64
	if req.Before != "" {
		id, err := strconv.ParseInt(req.Before, 10, 64)
		if err == nil {
			beforeID = &id
		}
	}
	if req.After != "" {
		id, err := strconv.ParseInt(req.After, 10, 64)
		if err == nil {
			afterID = &id
		}
	}

	messages, hasMore, err := h.service.ListMessages(ctx, req.RoomId, beforeID, afterID, limit)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	var lastReadMessageID string
	if h.readTrackingSvc != nil {
		lastRead, err := h.readTrackingSvc.GetRoomLastReadMessageID(ctx, userUUID, roomUUID)
		if err == nil && lastRead > 0 {
			lastReadMessageID = strconv.FormatInt(lastRead, 10)
		}
	}

	protoMessages := make([]*commonv1.Message, len(messages))
	for i, msg := range messages {
		protoMessages[i] = toProtoMessage(msg)
	}

	return &chatv1.ListMessagesResponse{
		Messages:          protoMessages,
		HasMore:           hasMore,
		LastReadMessageId: lastReadMessageID,
	}, nil
}

func (h *Handler) GetMessage(ctx context.Context, req *chatv1.GetMessageRequest) (*chatv1.GetMessageResponse, error) {
	if req.RoomId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and message_id are required"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	msg, err := h.service.GetMessage(ctx, req.RoomId, messageID)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.GetMessageResponse{
		Message: toProtoMessage(msg),
	}, nil
}

func (h *Handler) AddReaction(ctx context.Context, req *chatv1.AddReactionRequest) (*chatv1.AddReactionResponse, error) {
	if req.RoomId == "" || req.MessageId == "" || req.Emoji == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id, message_id and emoji are required"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	reaction, err := h.service.AddReaction(ctx, req.RoomId, messageID, req.Emoji)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.AddReactionResponse{
		Reaction: &commonv1.MessageReaction{
			Id:        reaction.ID.String(),
			MessageId: strconv.FormatInt(reaction.MessageID, 10),
			UserId:    reaction.UserID.String(),
			Emoji:     reaction.Emoji,
			CreatedAt: timestamppb.New(reaction.CreatedAt),
		},
	}, nil
}

func (h *Handler) RemoveReaction(ctx context.Context, req *chatv1.RemoveReactionRequest) (*chatv1.EmptyResponse, error) {
	if req.RoomId == "" || req.MessageId == "" || req.Emoji == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id, message_id and emoji are required"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	if err := h.service.RemoveReaction(ctx, req.RoomId, messageID, req.Emoji); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.EmptyResponse{}, nil
}

func (h *Handler) PinMessage(ctx context.Context, req *chatv1.PinMessageRequest) (*chatv1.EmptyResponse, error) {
	if req.RoomId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and message_id are required"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	if err := h.service.PinMessage(ctx, req.RoomId, messageID); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.EmptyResponse{}, nil
}

func (h *Handler) UnpinMessage(ctx context.Context, req *chatv1.UnpinMessageRequest) (*chatv1.EmptyResponse, error) {
	if req.RoomId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and message_id are required"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	if err := h.service.UnpinMessage(ctx, req.RoomId, messageID); err != nil {
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
		protoMessages[i] = toProtoMessage(msg)
	}

	return &chatv1.ListPinnedMessagesResponse{
		Messages: protoMessages,
	}, nil
}

func (h *Handler) GetThread(ctx context.Context, req *chatv1.GetThreadRequest) (*chatv1.GetThreadResponse, error) {
	if req.RoomId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and message_id are required"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	limit := int(req.Limit)
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var beforeID *int64
	if req.Before != "" {
		id, err := strconv.ParseInt(req.Before, 10, 64)
		if err == nil {
			beforeID = &id
		}
	}

	parent, replies, hasMore, err := h.service.GetThread(ctx, req.RoomId, messageID, beforeID, limit)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoReplies := make([]*commonv1.Message, len(replies))
	for i, msg := range replies {
		protoReplies[i] = toProtoMessage(msg)
	}

	return &chatv1.GetThreadResponse{
		Parent:  toProtoMessage(parent),
		Replies: protoReplies,
		HasMore: hasMore,
	}, nil
}

func (h *Handler) SearchMessages(ctx context.Context, req *chatv1.SearchMessagesRequest) (*chatv1.SearchMessagesResponse, error) {
	if req.RoomId == "" || req.Query == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and query are required"))
	}

	limit := int(req.Limit)
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var beforeID *int64
	if req.Before != "" {
		id, err := strconv.ParseInt(req.Before, 10, 64)
		if err == nil {
			beforeID = &id
		}
	}

	messages, hasMore, err := h.service.SearchMessages(ctx, req.RoomId, req.Query, beforeID, limit)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoMessages := make([]*commonv1.Message, len(messages))
	for i, msg := range messages {
		protoMessages[i] = toProtoMessage(msg)
	}

	return &chatv1.SearchMessagesResponse{
		Messages: protoMessages,
		HasMore:  hasMore,
	}, nil
}

func (h *Handler) MarkAsRead(ctx context.Context, req *chatv1.MarkAsReadRequest) (*chatv1.MarkAsReadResponse, error) {
	if req.RoomId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and message_id are required"))
	}

	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	roomUUID, err := uuid.Parse(req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid room id"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	lastRead, unreadCount, err := h.readTrackingSvc.MarkRoomAsRead(ctx, userUUID, roomUUID, messageID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to mark as read", err))
	}

	return &chatv1.MarkAsReadResponse{
		LastReadMessageId: strconv.FormatInt(lastRead, 10),
		UnreadCount:       unreadCount,
	}, nil
}

func (h *Handler) GetUnreadCounts(ctx context.Context, req *chatv1.GetUnreadCountsRequest) (*chatv1.GetUnreadCountsResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	infos, total, err := h.readTrackingSvc.GetAllRoomUnreadCounts(ctx, userUUID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to get unread counts", err))
	}

	rooms := make([]*chatv1.RoomUnreadInfo, len(infos))
	for i, info := range infos {
		rooms[i] = &chatv1.RoomUnreadInfo{
			RoomId:            info.RoomID.String(),
			UnreadCount:       info.UnreadCount,
			LastReadMessageId: strconv.FormatInt(info.LastReadMessageID, 10),
			LatestMessageId:   strconv.FormatInt(info.LatestMessageID, 10),
			LatestMessageAt:   timestamppb.New(info.LatestMessageAt),
		}
	}

	return &chatv1.GetUnreadCountsResponse{
		Rooms:       rooms,
		TotalUnread: total,
	}, nil
}

func (h *Handler) StartTyping(ctx context.Context, req *chatv1.StartTypingRequest) (*chatv1.EmptyResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	roomUUID, err := uuid.Parse(req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid room id"))
	}

	if err := h.typingSvc.StartTypingInRoom(ctx, userUUID, roomUUID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to start typing", err))
	}

	return &chatv1.EmptyResponse{}, nil
}

func (h *Handler) StopTyping(ctx context.Context, req *chatv1.StopTypingRequest) (*chatv1.EmptyResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	roomUUID, err := uuid.Parse(req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid room id"))
	}

	if err := h.typingSvc.StopTypingInRoom(ctx, userUUID, roomUUID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to stop typing", err))
	}

	return &chatv1.EmptyResponse{}, nil
}

func toProtoMessage(msg *Message) *commonv1.Message {
	if msg == nil {
		return nil
	}

	protoMsg := &commonv1.Message{
		Id:         strconv.FormatInt(msg.ID, 10),
		RoomId:     msg.RoomID.String(),
		AuthorId:   msg.AuthorID.String(),
		Content:    msg.Content,
		CreatedAt:  timestamppb.New(msg.CreatedAt),
		ReplyCount: msg.ReplyCount,
		Pinned:     msg.Pinned,
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

	for _, att := range msg.Attachments {
		protoMsg.Attachments = append(protoMsg.Attachments, &commonv1.MessageAttachment{
			Id:          att.ID.String(),
			Url:         att.URL,
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Size:        att.Size,
			Width:       int32(att.Width),
			Height:      int32(att.Height),
			CreatedAt:   timestamppb.New(att.CreatedAt),
		})
	}

	for _, mention := range msg.Mentions {
		protoMsg.Mentions = append(protoMsg.Mentions, mention.String())
	}

	for _, reaction := range msg.Reactions {
		protoMsg.Reactions = append(protoMsg.Reactions, &commonv1.MessageReaction{
			Id:        reaction.ID.String(),
			MessageId: strconv.FormatInt(reaction.MessageID, 10),
			UserId:    reaction.UserID.String(),
			Emoji:     reaction.Emoji,
			CreatedAt: timestamppb.New(reaction.CreatedAt),
		})
	}

	return protoMsg
}
