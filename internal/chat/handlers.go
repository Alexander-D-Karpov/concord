package chat

import (
	"context"
	"strconv"

	chatv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/chat/v1"
	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/readtracking"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/typing"
	"github.com/Alexander-D-Karpov/concord/internal/storage"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler is the ChatService gRPC server. It validates and unmarshals requests,
// stores attachment bytes through storage, and delegates domain logic to
// service; read tracking and typing are handled by their own services.
type Handler struct {
	chatv1.UnimplementedChatServiceServer
	service         *Service
	storage         *storage.Storage
	readTrackingSvc *readtracking.Service
	typingSvc       *typing.Service
}

// NewHandler constructs the ChatService handler with all its collaborators
// supplied up front (unlike the dm handler, which injects some via setters).
func NewHandler(service *Service, storage *storage.Storage, readTrackingSvc *readtracking.Service, typingSvc *typing.Service) *Handler {
	return &Handler{
		service:         service,
		storage:         storage,
		readTrackingSvc: readTrackingSvc,
		typingSvc:       typingSvc,
	}
}

// SendMessage handles the SendMessage RPC: it requires room_id plus content or
// attachments, parses the optional reply/mention/forward fields, uploads each
// non-empty attachment through storage (using the stored dimensions when the
// backend detected them), and forwards a SendMessageParams to the service.
// reply_mention_author defaults to true unless the request overrides it.
func (h *Handler) SendMessage(ctx context.Context, req *chatv1.SendMessageRequest) (*chatv1.SendMessageResponse, error) {
	if req.RoomId == "" || (req.Content == "" && len(req.Attachments) == 0) {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and content or attachments are required"))
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

	var attachments []Attachment
	for _, att := range req.Attachments {
		if len(att.Data) == 0 {
			continue
		}

		fileInfo, err := h.storage.Store(ctx, att.Data, att.Filename, att.ContentType)
		if err != nil {
			return nil, errors.ToGRPCError(errors.BadRequest("failed to store attachment: " + err.Error()))
		}

		width := int(att.Width)
		height := int(att.Height)
		if fileInfo.Width > 0 {
			width = fileInfo.Width
			height = fileInfo.Height
		}

		attachments = append(attachments, Attachment{
			ID:          uuid.MustParse(fileInfo.ID),
			URL:         fileInfo.URL,
			Filename:    att.Filename,
			ContentType: fileInfo.ContentType,
			Size:        fileInfo.Size,
			Width:       width,
			Height:      height,
		})
	}

	params := SendMessageParams{
		RoomID:             req.RoomId,
		Content:            req.Content,
		ReplyToID:          replyToID,
		MentionIDs:         mentionIDs,
		Attachments:        attachments,
		ReplyMentionAuthor: true,
	}

	if req.MediaGroupId != "" {
		params.MediaGroupID = &req.MediaGroupId
	}

	if req.ReplyQuotedContent != "" {
		params.ReplyQuotedContent = &req.ReplyQuotedContent
	}

	if req.ForwardInfo != nil {
		if req.ForwardInfo.OriginalAuthorId != "" {
			if uid, err := uuid.Parse(req.ForwardInfo.OriginalAuthorId); err == nil {
				params.ForwardFromUserID = &uid
			}
		}
		if req.ForwardInfo.OriginalAuthorName != "" {
			params.ForwardFromUserName = &req.ForwardInfo.OriginalAuthorName
		}
		if req.ForwardInfo.OriginalRoomId != "" {
			if rid, err := uuid.Parse(req.ForwardInfo.OriginalRoomId); err == nil {
				params.ForwardFromRoomID = &rid
			}
		}
		if req.ForwardInfo.OriginalMessageId != "" {
			if mid, err := strconv.ParseInt(req.ForwardInfo.OriginalMessageId, 10, 64); err == nil {
				params.ForwardFromMsgID = &mid
			}
		}
		if req.ForwardInfo.OriginalTimestamp != nil {
			ts := req.ForwardInfo.OriginalTimestamp.AsTime()
			params.ForwardOriginalTS = &ts
		}
	}

	if req.ReplyMentionAuthor != nil {
		params.ReplyMentionAuthor = *req.ReplyMentionAuthor
	}

	msg, err := h.service.SendMessage(ctx, params)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &chatv1.SendMessageResponse{
		Message: toProtoMessage(msg),
	}, nil
}

// ListMessagesSince returns messages newer than after_message_id (forward
// paging for catch-up/sync). limit is clamped to (0,200] and defaults to 100.
func (h *Handler) ListMessagesSince(ctx context.Context, req *chatv1.ListMessagesSinceRequest) (*chatv1.ListMessagesSinceResponse, error) {
	if req.RoomId == "" || req.AfterMessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and after_message_id are required"))
	}

	afterID, err := strconv.ParseInt(req.AfterMessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid after_message_id"))
	}

	limit := int(req.Limit)
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	messages, hasMore, err := h.service.ListMessages(ctx, req.RoomId, nil, &afterID, limit)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoMessages := make([]*commonv1.Message, len(messages))
	for i, msg := range messages {
		protoMessages[i] = toProtoMessage(msg)
	}

	return &chatv1.ListMessagesSinceResponse{
		Messages: protoMessages,
		HasMore:  hasMore,
	}, nil
}

// EditMessage handles the EditMessage RPC, requiring room_id, message_id, and
// non-empty content; author-only enforcement lives in the service.
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

// DeleteMessage handles the DeleteMessage RPC (soft delete); the service
// enforces that only the author may delete.
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

// ListMessages returns a page of a room's messages with before/after cursors and
// also enriches the response with the caller's last-read message id when a read-
// tracking service is available (a read-tracking failure is ignored, not fatal).
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

// GetMessage handles the GetMessage RPC for a single message by id within a room.
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

// AddReaction handles the AddReaction RPC, requiring room_id, message_id, and a
// non-empty emoji, and returns the created reaction.
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

// RemoveReaction handles the RemoveReaction RPC, removing the caller's emoji
// reaction from a message.
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

// PinMessage handles the PinMessage RPC; the service records the caller as the
// pinner and broadcasts the pin.
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

// UnpinMessage handles the UnpinMessage RPC, removing a room's pin for a message.
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

// ListPinnedMessages handles the ListPinnedMessages RPC for a room.
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

// GetThread returns a parent message and a page of its replies. limit is clamped
// to (0,100] (default 50); the before cursor is passed through as the reply
// offset the service expects.
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

// SearchMessages handles the SearchMessages RPC, requiring room_id and query;
// limit is clamped to (0,100] and defaults to 50.
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

// MarkAsRead advances the caller's last-read pointer in a room to message_id via
// the read-tracking service, returning the new last-read id and unread count.
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

// GetUnreadCounts returns the caller's per-room unread counts and total, sourced
// from the read-tracking service.
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

// StartTyping records that the caller began typing in a room via the typing
// service, which fans out the transient typing indicator to other members.
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

// StopTyping clears the caller's typing indicator in a room via the typing
// service.
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

// toProtoMessage converts a domain Message to the wire commonv1.Message,
// returning nil for a nil input. It stringifies the int64 Snowflake id, maps
// DeletedAt to the Deleted flag, and copies attachments, mentions, reactions,
// and forward metadata; reply_mention_author is always emitted (as a pointer).
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

	if msg.ForwardFromUserID != nil || msg.ForwardFromUserName != nil || msg.ForwardFromRoomID != nil || msg.ForwardFromMsgID != nil || msg.ForwardOriginalTS != nil {
		protoMsg.ForwardInfo = &commonv1.ForwardInfo{}

		if msg.ForwardFromUserID != nil {
			protoMsg.ForwardInfo.OriginalAuthorId = msg.ForwardFromUserID.String()
		}
		if msg.ForwardFromUserName != nil {
			protoMsg.ForwardInfo.OriginalAuthorName = *msg.ForwardFromUserName
		}
		if msg.ForwardFromRoomID != nil {
			protoMsg.ForwardInfo.OriginalRoomId = msg.ForwardFromRoomID.String()
		}
		if msg.ForwardFromMsgID != nil {
			protoMsg.ForwardInfo.OriginalMessageId = strconv.FormatInt(*msg.ForwardFromMsgID, 10)
		}
		if msg.ForwardOriginalTS != nil {
			protoMsg.ForwardInfo.OriginalTimestamp = timestamppb.New(*msg.ForwardOriginalTS)
		}
	}

	if msg.MediaGroupID != nil {
		protoMsg.MediaGroupId = *msg.MediaGroupID
	}

	if msg.ReplyQuotedContent != nil {
		protoMsg.ReplyQuotedContent = *msg.ReplyQuotedContent
	}

	replyMentionAuthor := msg.ReplyMentionAuthor
	protoMsg.ReplyMentionAuthor = &replyMentionAuthor

	protoMsg.EditCount = msg.EditCount

	return protoMsg
}

// GetMessageEditHistory returns the recorded prior versions of a message,
// each entry carrying the previous content, edit timestamp, and version number.
func (h *Handler) GetMessageEditHistory(ctx context.Context, req *chatv1.GetMessageEditHistoryRequest) (*chatv1.GetMessageEditHistoryResponse, error) {
	if req.RoomId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and message_id are required"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	entries, err := h.service.GetEditHistory(ctx, req.RoomId, messageID)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	out := make([]*commonv1.EditHistoryEntry, len(entries))
	for i, e := range entries {
		out[i] = &commonv1.EditHistoryEntry{
			Id:              e.ID,
			PreviousContent: e.PreviousContent,
			EditedAt:        timestamppb.New(e.EditedAt),
			Version:         int32(e.Version),
		}
	}
	return &chatv1.GetMessageEditHistoryResponse{Entries: out}, nil
}
