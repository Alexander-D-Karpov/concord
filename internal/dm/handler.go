package dm

import (
	"context"
	"strconv"

	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	dmv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/dm/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/readtracking"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/typing"
	"github.com/Alexander-D-Karpov/concord/internal/storage"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler is the DMService gRPC server. It validates requests, stores attachment
// bytes through storage, and delegates domain logic to service. readTrackingSvc
// and typingSvc are optional and injected after construction via their setters;
// methods that use them nil-guard where read/typing is optional but assume they
// are set for the read/typing RPCs.
type Handler struct {
	dmv1.UnimplementedDMServiceServer
	service         *Service
	storage         *storage.Storage
	readTrackingSvc *readtracking.Service
	typingSvc       *typing.Service
}

// NewHandler constructs the DMService handler with only the service and storage.
// readTrackingSvc and typingSvc start nil and must be wired via
// SetReadTrackingService/SetTypingService before the read-tracking/typing RPCs
// are used.
func NewHandler(service *Service, storageService *storage.Storage) *Handler {
	return &Handler{
		service: service,
		storage: storageService,
	}
}

// SetReadTrackingService injects the read-tracking dependency post-construction.
func (h *Handler) SetReadTrackingService(svc *readtracking.Service) {
	h.readTrackingSvc = svc
}

// SetTypingService injects the typing-indicator dependency post-construction.
func (h *Handler) SetTypingService(svc *typing.Service) {
	h.typingSvc = svc
}

// CreateDM handles the CreateDM RPC, returning (creating if needed) the channel
// between the caller and req.UserId.
func (h *Handler) CreateDM(ctx context.Context, req *dmv1.CreateDMRequest) (*dmv1.CreateDMResponse, error) {
	if req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("user_id is required"))
	}

	channel, err := h.service.GetOrCreateDM(ctx, req.UserId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.CreateDMResponse{
		Channel: dmChannelToProto(channel),
	}, nil
}

// GetDMChannel handles the GetDMChannel RPC, returning a channel by id. Note the
// service does not participant-check this lookup.
func (h *Handler) GetDMChannel(ctx context.Context, req *dmv1.GetDMChannelRequest) (*dmv1.GetDMChannelResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}

	channel, err := h.service.GetChannel(ctx, req.ChannelId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.GetDMChannelResponse{
		Channel: dmChannelToProto(channel),
	}, nil
}

// ListDMChannels returns the caller's DM channels, manually attaching each
// channel's single other-participant info (with live presence) from the joined
// query result since dmChannelToProto emits an empty participant list.
func (h *Handler) ListDMChannels(ctx context.Context, req *dmv1.ListDMChannelsRequest) (*dmv1.ListDMChannelsResponse, error) {
	channels, err := h.service.ListDMs(ctx)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoChannels := make([]*dmv1.DMChannel, len(channels))
	for i, ch := range channels {
		// Populate channel basic info
		p := dmChannelToProto(ch.Channel)

		// IMPORTANT: Manually populate the participant info from the query result
		p.Participants = []*dmv1.DMParticipant{
			{
				UserId:      ch.OtherUserID.String(),
				Handle:      ch.OtherUserHandle,
				DisplayName: ch.OtherUserDisplay,
				AvatarUrl:   ch.OtherUserAvatar,
				Status:      ch.OtherUserStatus,
			},
		}

		protoChannels[i] = p
	}

	return &dmv1.ListDMChannelsResponse{
		Channels: protoChannels,
	}, nil
}

// SendDM handles the SendDM RPC: it requires content or attachments, uploads
// each non-empty attachment through storage (using detected image dimensions
// when available), and delegates to the service to persist and broadcast.
func (h *Handler) SendDM(ctx context.Context, req *dmv1.SendDMRequest) (*dmv1.SendDMResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}
	if req.Content == "" && len(req.Attachments) == 0 {
		return nil, errors.ToGRPCError(errors.BadRequest("content or attachments are required"))
	}

	var attachments []DMAttachment
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

		attachments = append(attachments, DMAttachment{
			ID:          uuid.MustParse(fileInfo.ID),
			URL:         fileInfo.URL,
			Filename:    att.Filename,
			ContentType: fileInfo.ContentType,
			Size:        fileInfo.Size,
			Width:       width,
			Height:      height,
		})
	}

	msg, err := h.service.SendMessage(ctx, req.ChannelId, req.Content, req.ReplyToId, attachments, nil)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.SendDMResponse{
		Message: dmMessageToProto(msg),
	}, nil
}

// EditDM handles the EditDM RPC; the service enforces author-only editing.
func (h *Handler) EditDM(ctx context.Context, req *dmv1.EditDMRequest) (*dmv1.EditDMResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and message_id are required"))
	}
	if req.Content == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("content is required"))
	}

	msg, err := h.service.EditMessage(ctx, req.ChannelId, req.MessageId, req.Content)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.EditDMResponse{
		Message: dmMessageToProto(msg),
	}, nil
}

// DeleteDM handles the DeleteDM RPC (soft delete); the service enforces
// author-only deletion.
func (h *Handler) DeleteDM(ctx context.Context, req *dmv1.DeleteDMRequest) (*dmv1.EmptyResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and message_id are required"))
	}

	if err := h.service.DeleteMessage(ctx, req.ChannelId, req.MessageId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.EmptyResponse{}, nil
}

// ListDMMessages returns a page of a channel's messages with before/after
// cursors and, when a read-tracking service is wired, the caller's last-read
// message id (read-tracking failures are ignored).
func (h *Handler) ListDMMessages(ctx context.Context, req *dmv1.ListDMMessagesRequest) (*dmv1.ListDMMessagesResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}

	userID := interceptor.GetUserID(ctx)
	userUUID, _ := uuid.Parse(userID)
	channelUUID, _ := uuid.Parse(req.ChannelId)

	limit := int(req.Limit)
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var beforeID, afterID *string
	if req.Before != "" {
		beforeID = &req.Before
	}
	if req.After != "" {
		afterID = &req.After
	}

	messages, hasMore, err := h.service.ListMessages(ctx, req.ChannelId, beforeID, afterID, limit)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	var lastReadMessageID string
	if h.readTrackingSvc != nil {
		lastRead, err := h.readTrackingSvc.GetDMLastReadMessageID(ctx, userUUID, channelUUID)
		if err == nil && lastRead > 0 {
			lastReadMessageID = strconv.FormatInt(lastRead, 10)
		}
	}

	protoMessages := make([]*dmv1.DMMessage, len(messages))
	for i, msg := range messages {
		protoMessages[i] = dmMessageToProto(msg)
	}

	return &dmv1.ListDMMessagesResponse{
		Messages:          protoMessages,
		HasMore:           hasMore,
		LastReadMessageId: lastReadMessageID,
	}, nil
}

// MarkDMAsRead advances the caller's last-read pointer in a channel to
// message_id, returning the new pointer and unread count. It assumes the
// read-tracking service is wired.
func (h *Handler) MarkDMAsRead(ctx context.Context, req *dmv1.MarkDMAsReadRequest) (*dmv1.MarkDMAsReadResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and message_id are required"))
	}

	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	channelUUID, err := uuid.Parse(req.ChannelId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid channel id"))
	}

	messageID, err := strconv.ParseInt(req.MessageId, 10, 64)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid message_id"))
	}

	lastRead, unreadCount, err := h.readTrackingSvc.MarkDMAsRead(ctx, userUUID, channelUUID, messageID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to mark as read", err))
	}

	return &dmv1.MarkDMAsReadResponse{
		LastReadMessageId: strconv.FormatInt(lastRead, 10),
		UnreadCount:       unreadCount,
	}, nil
}

// StartDMTyping records that the caller began typing in a channel and notifies
// the other participant (resolved via the service) through the typing service.
func (h *Handler) StartDMTyping(ctx context.Context, req *dmv1.StartDMTypingRequest) (*dmv1.EmptyResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}

	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	channelUUID, err := uuid.Parse(req.ChannelId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid channel id"))
	}

	otherUserID, err := h.service.GetOtherParticipant(ctx, channelUUID, userUUID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to get other participant", err))
	}

	if err := h.typingSvc.StartTypingInDM(ctx, userUUID, channelUUID, otherUserID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to start typing", err))
	}

	return &dmv1.EmptyResponse{}, nil
}

// StopDMTyping clears the caller's typing indicator in a channel and notifies
// the other participant.
func (h *Handler) StopDMTyping(ctx context.Context, req *dmv1.StopDMTypingRequest) (*dmv1.EmptyResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}

	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}

	channelUUID, err := uuid.Parse(req.ChannelId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid channel id"))
	}

	otherUserID, err := h.service.GetOtherParticipant(ctx, channelUUID, userUUID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to get other participant", err))
	}

	if err := h.typingSvc.StopTypingInDM(ctx, userUUID, channelUUID, otherUserID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to stop typing", err))
	}

	return &dmv1.EmptyResponse{}, nil
}

// JoinDMCall joins the caller to the channel's call (starting one if none is
// active) and returns the UDP/TCP endpoint, voice token, codec, crypto suite,
// and the existing participants.
func (h *Handler) JoinDMCall(ctx context.Context, req *dmv1.JoinDMCallRequest) (*dmv1.JoinDMCallResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}

	assignment, participants, err := h.service.JoinCall(ctx, req.ChannelId, req.AudioOnly)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoParticipants := make([]*callv1.Participant, len(participants))
	for i, p := range participants {
		protoParticipants[i] = &callv1.Participant{
			UserId:       p.UserID,
			Muted:        p.Muted,
			VideoEnabled: p.VideoEnabled,
		}
	}

	return &dmv1.JoinDMCallResponse{
		Endpoint: &callv1.UdpEndpoint{
			Host: assignment.Endpoint.Host,
			Port: uint32(assignment.Endpoint.Port),
		},
		ServerId:   assignment.ServerID,
		VoiceToken: assignment.VoiceToken,
		Codec: &callv1.CodecHint{
			Audio: assignment.Codec.Audio,
			Video: assignment.Codec.Video,
		},
		Crypto: &callv1.CryptoSuite{
			Aead:        assignment.Crypto.AEAD,
			KeyId:       assignment.Crypto.KeyID,
			KeyMaterial: assignment.Crypto.KeyMaterial,
			NonceBase:   assignment.Crypto.NonceBase,
		},
		Participants: protoParticipants,
		ExpiresIn:    uint32(assignment.ExpiresIn),
		TcpEndpoint:  dmTCPEndpoint(assignment.TCPEndpoint.Host, assignment.TCPEndpoint.Port),
	}, nil
}

// dmTCPEndpoint builds a proto TCP fallback endpoint, returning nil when host or
// port is unset so the field is omitted rather than sent empty.
func dmTCPEndpoint(host string, port int) *callv1.UdpEndpoint {
	if host == "" || port == 0 {
		return nil
	}
	return &callv1.UdpEndpoint{Host: host, Port: uint32(port)}
}

// LeaveDMCall removes the caller from the channel's call, ending it when no
// participants remain (handled by the service).
func (h *Handler) LeaveDMCall(ctx context.Context, req *dmv1.LeaveDMCallRequest) (*dmv1.EmptyResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}

	if err := h.service.LeaveCall(ctx, req.ChannelId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.EmptyResponse{}, nil
}

// dmChannelToProto converts a channel to its wire form with an empty participant
// list; callers that have the other-user profile (e.g. ListDMChannels) populate
// Participants afterward.
func dmChannelToProto(ch *DMChannel) *dmv1.DMChannel {
	return &dmv1.DMChannel{
		Id:           ch.ID.String(),
		Participants: []*dmv1.DMParticipant{}, // Initially empty, populated in loop
		CreatedAt:    timestamppb.New(ch.CreatedAt),
	}
}

// dmMessageToProto converts a DMMessage to the DM-specific wire message,
// stringifying the int64 id, mapping DeletedAt to the Deleted flag, and copying
// attachments, reactions, mentions, read receipts, and forward metadata.
func dmMessageToProto(msg *DMMessage) *dmv1.DMMessage {
	proto := &dmv1.DMMessage{
		Id:         strconv.FormatInt(msg.ID, 10),
		ChannelId:  msg.ChannelID.String(),
		AuthorId:   msg.AuthorID.String(),
		Content:    msg.Content,
		CreatedAt:  timestamppb.New(msg.CreatedAt),
		Deleted:    msg.DeletedAt != nil,
		ReplyCount: msg.ReplyCount,
		Pinned:     msg.Pinned,
		EditCount:  msg.EditCount,
	}

	if msg.EditedAt != nil {
		proto.EditedAt = timestamppb.New(*msg.EditedAt)
	}
	if msg.ReplyToID != nil {
		proto.ReplyToId = strconv.FormatInt(*msg.ReplyToID, 10)
	}
	if msg.MediaGroupID != nil {
		proto.MediaGroupId = *msg.MediaGroupID
	}
	if msg.ReplyQuotedContent != nil {
		proto.ReplyQuotedContent = *msg.ReplyQuotedContent
	}
	rma := msg.ReplyMentionAuthor
	proto.ReplyMentionAuthor = &rma

	for _, att := range msg.Attachments {
		proto.Attachments = append(proto.Attachments, &dmv1.DMAttachment{
			Id:          att.ID.String(),
			Url:         att.URL,
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Size:        att.Size,
			Width:       int32(att.Width),
			Height:      int32(att.Height),
		})
	}
	for _, r := range msg.Reactions {
		proto.Reactions = append(proto.Reactions, &commonv1.MessageReaction{
			Id:        r.ID.String(),
			MessageId: strconv.FormatInt(r.MessageID, 10),
			UserId:    r.UserID.String(),
			Emoji:     r.Emoji,
			CreatedAt: timestamppb.New(r.CreatedAt),
		})
	}
	for _, m := range msg.Mentions {
		proto.Mentions = append(proto.Mentions, m.String())
	}
	for _, r := range msg.ReadBy {
		proto.ReadBy = append(proto.ReadBy, &dmv1.ReadReceipt{
			UserId: r.UserID.String(),
			ReadAt: timestamppb.New(r.ReadAt),
		})
	}

	if msg.ForwardFromUserID != nil || msg.ForwardFromUserName != nil ||
		msg.ForwardFromChannelID != nil || msg.ForwardFromMsgID != nil || msg.ForwardOriginalTS != nil {
		proto.ForwardInfo = &commonv1.ForwardInfo{}
		if msg.ForwardFromUserID != nil {
			proto.ForwardInfo.OriginalAuthorId = msg.ForwardFromUserID.String()
		}
		if msg.ForwardFromUserName != nil {
			proto.ForwardInfo.OriginalAuthorName = *msg.ForwardFromUserName
		}
		if msg.ForwardFromChannelID != nil {
			proto.ForwardInfo.OriginalChannelId = msg.ForwardFromChannelID.String()
		}
		if msg.ForwardFromMsgID != nil {
			proto.ForwardInfo.OriginalMessageId = strconv.FormatInt(*msg.ForwardFromMsgID, 10)
		}
		if msg.ForwardOriginalTS != nil {
			proto.ForwardInfo.OriginalTimestamp = timestamppb.New(*msg.ForwardOriginalTS)
		}
	}

	return proto
}

// dmMessageToCommonProto converts a DMMessage to the shared commonv1.Message
// shape (used for edit events), carrying the channel id in the RoomId field. It
// includes only the core fields, not attachments/reactions.
func dmMessageToCommonProto(msg *DMMessage) *commonv1.Message {
	proto := &commonv1.Message{
		Id:        strconv.FormatInt(msg.ID, 10),
		RoomId:    msg.ChannelID.String(),
		AuthorId:  msg.AuthorID.String(),
		Content:   msg.Content,
		CreatedAt: timestamppb.New(msg.CreatedAt),
	}

	if msg.EditedAt != nil {
		proto.EditedAt = timestamppb.New(*msg.EditedAt)
	}

	if msg.ReplyToID != nil {
		proto.ReplyToId = strconv.FormatInt(*msg.ReplyToID, 10)
	}

	return proto
}

// AddDMReaction handles the AddDMReaction RPC and echoes back the created
// reaction.
func (h *Handler) AddDMReaction(ctx context.Context, req *dmv1.AddDMReactionRequest) (*dmv1.AddDMReactionResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" || req.Emoji == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id, message_id, emoji required"))
	}
	reaction, err := h.service.AddReactionAndReturn(ctx, req.ChannelId, req.MessageId, req.Emoji)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &dmv1.AddDMReactionResponse{
		Reaction: &commonv1.MessageReaction{
			Id:        reaction.ID.String(),
			MessageId: strconv.FormatInt(reaction.MessageID, 10),
			UserId:    reaction.UserID.String(),
			Emoji:     reaction.Emoji,
			CreatedAt: timestamppb.New(reaction.CreatedAt),
		},
	}, nil
}

// RemoveDMReaction handles the RemoveDMReaction RPC.
func (h *Handler) RemoveDMReaction(ctx context.Context, req *dmv1.RemoveDMReactionRequest) (*dmv1.EmptyResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" || req.Emoji == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id, message_id, emoji required"))
	}
	if err := h.service.RemoveReaction(ctx, req.ChannelId, req.MessageId, req.Emoji); err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &dmv1.EmptyResponse{}, nil
}

// PinDMMessage handles the PinDMMessage RPC.
func (h *Handler) PinDMMessage(ctx context.Context, req *dmv1.PinDMMessageRequest) (*dmv1.EmptyResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and message_id required"))
	}
	if err := h.service.PinMessage(ctx, req.ChannelId, req.MessageId); err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &dmv1.EmptyResponse{}, nil
}

// UnpinDMMessage handles the UnpinDMMessage RPC.
func (h *Handler) UnpinDMMessage(ctx context.Context, req *dmv1.UnpinDMMessageRequest) (*dmv1.EmptyResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and message_id required"))
	}
	if err := h.service.UnpinMessage(ctx, req.ChannelId, req.MessageId); err != nil {
		return nil, errors.ToGRPCError(err)
	}
	return &dmv1.EmptyResponse{}, nil
}

// ListDMPinned handles the ListDMPinned RPC, returning a channel's pinned
// messages.
func (h *Handler) ListDMPinned(ctx context.Context, req *dmv1.ListDMPinnedRequest) (*dmv1.ListDMPinnedResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id required"))
	}
	msgs, err := h.service.ListPinnedMessages(ctx, req.ChannelId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	out := make([]*dmv1.DMMessage, len(msgs))
	for i, m := range msgs {
		out[i] = dmMessageToProto(m)
	}
	return &dmv1.ListDMPinnedResponse{Messages: out}, nil
}

// GetDMThread returns a parent DM message plus a page of its replies. limit
// defaults to 50; req.Cursor is the numeric reply offset, and NextCursor carries
// the offset for the next page.
func (h *Handler) GetDMThread(ctx context.Context, req *dmv1.GetDMThreadRequest) (*dmv1.GetDMThreadResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and message_id required"))
	}
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}
	parent, replies, next, err := h.service.GetThreadWithParent(ctx, req.ChannelId, req.MessageId, limit, req.Cursor)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	out := make([]*dmv1.DMMessage, len(replies))
	for i, r := range replies {
		out[i] = dmMessageToProto(r)
	}
	return &dmv1.GetDMThreadResponse{
		Parent:     dmMessageToProto(parent),
		Replies:    out,
		NextCursor: next,
	}, nil
}

// SearchDMMessages searches a channel's messages. It requests limit+1 from the
// service to compute HasMore, trimming the surplus before returning.
func (h *Handler) SearchDMMessages(ctx context.Context, req *dmv1.SearchDMMessagesRequest) (*dmv1.SearchDMMessagesResponse, error) {
	if req.ChannelId == "" || req.Query == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and query required"))
	}
	limit := int(req.Limit)
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	msgs, err := h.service.SearchMessages(ctx, req.ChannelId, req.Query, limit+1)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}
	out := make([]*dmv1.DMMessage, len(msgs))
	for i, m := range msgs {
		out[i] = dmMessageToProto(m)
	}
	return &dmv1.SearchDMMessagesResponse{Messages: out, HasMore: hasMore}, nil
}

// GetDMUnreadCounts returns the caller's per-channel unread counts and total,
// from the read-tracking service.
func (h *Handler) GetDMUnreadCounts(ctx context.Context, req *dmv1.GetDMUnreadCountsRequest) (*dmv1.GetDMUnreadCountsResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid user id"))
	}
	infos, total, err := h.readTrackingSvc.GetAllDMUnreadCounts(ctx, userUUID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to get unread counts", err))
	}
	channels := make([]*dmv1.DMUnreadInfo, len(infos))
	for i, info := range infos {
		channels[i] = &dmv1.DMUnreadInfo{
			ChannelId:         info.ChannelID.String(),
			UnreadCount:       info.UnreadCount,
			LastReadMessageId: strconv.FormatInt(info.LastReadMessageID, 10),
			LatestMessageId:   strconv.FormatInt(info.LatestMessageID, 10),
			LatestMessageAt:   timestamppb.New(info.LatestMessageAt),
		}
	}
	return &dmv1.GetDMUnreadCountsResponse{Channels: channels, TotalUnread: total}, nil
}

// ListDMMessagesSince returns messages newer than after_message_id (forward
// paging for catch-up). limit is clamped to (0,200] and defaults to 100.
func (h *Handler) ListDMMessagesSince(ctx context.Context, req *dmv1.ListDMMessagesSinceRequest) (*dmv1.ListDMMessagesSinceResponse, error) {
	if req.ChannelId == "" || req.AfterMessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and after_message_id required"))
	}
	limit := int(req.Limit)
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	afterID := &req.AfterMessageId
	msgs, hasMore, err := h.service.ListMessages(ctx, req.ChannelId, nil, afterID, limit)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}
	out := make([]*dmv1.DMMessage, len(msgs))
	for i, m := range msgs {
		out[i] = dmMessageToProto(m)
	}
	return &dmv1.ListDMMessagesSinceResponse{Messages: out, HasMore: hasMore}, nil
}

// GetDMEditHistory returns the recorded prior versions of a DM message.
func (h *Handler) GetDMEditHistory(ctx context.Context, req *dmv1.GetDMEditHistoryRequest) (*dmv1.GetDMEditHistoryResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and message_id required"))
	}
	entries, err := h.service.GetEditHistory(ctx, req.ChannelId, req.MessageId)
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
	return &dmv1.GetDMEditHistoryResponse{Entries: out}, nil
}
