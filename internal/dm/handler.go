package dm

import (
	"context"
	"strconv"

	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	dmv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/dm/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/readtracking"
	"github.com/Alexander-D-Karpov/concord/internal/storage"
	"github.com/Alexander-D-Karpov/concord/internal/typing"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	dmv1.UnimplementedDMServiceServer
	service         *Service
	storage         *storage.Storage
	readTrackingSvc *readtracking.Service
	typingSvc       *typing.Service
}

func NewHandler(service *Service, storageService *storage.Storage) *Handler {
	return &Handler{
		service: service,
		storage: storageService,
	}
}

func (h *Handler) SetReadTrackingService(svc *readtracking.Service) {
	h.readTrackingSvc = svc
}

func (h *Handler) SetTypingService(svc *typing.Service) {
	h.typingSvc = svc
}

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

func (h *Handler) SendDM(ctx context.Context, req *dmv1.SendDMRequest) (*dmv1.SendDMResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}
	if req.Content == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("content is required"))
	}

	msg, err := h.service.SendMessage(ctx, req.ChannelId, req.Content, req.ReplyToId, nil, nil)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.SendDMResponse{
		Message: dmMessageToProto(msg),
	}, nil
}

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

func (h *Handler) DeleteDM(ctx context.Context, req *dmv1.DeleteDMRequest) (*dmv1.EmptyResponse, error) {
	if req.ChannelId == "" || req.MessageId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id and message_id are required"))
	}

	if err := h.service.DeleteMessage(ctx, req.ChannelId, req.MessageId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.EmptyResponse{}, nil
}

func (h *Handler) ListDMMessages(ctx context.Context, req *dmv1.ListDMMessagesRequest) (*dmv1.ListDMMessagesResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}

	userID := interceptor.GetUserID(ctx)
	userUUID, _ := uuid.Parse(userID)
	channelUUID, _ := uuid.Parse(req.ChannelId)

	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}

	var beforeID, afterID *string
	if req.Before != "" {
		beforeID = &req.Before
	}
	if req.After != "" {
		afterID = &req.After
	}

	messages, err := h.service.ListMessages(ctx, req.ChannelId, beforeID, afterID, limit)
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
		HasMore:           len(messages) == limit,
		LastReadMessageId: lastReadMessageID,
	}, nil
}

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
	}, nil
}

func (h *Handler) LeaveDMCall(ctx context.Context, req *dmv1.LeaveDMCallRequest) (*dmv1.EmptyResponse, error) {
	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}

	if err := h.service.LeaveCall(ctx, req.ChannelId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.EmptyResponse{}, nil
}

func dmChannelToProto(ch *DMChannel) *dmv1.DMChannel {
	return &dmv1.DMChannel{
		Id:           ch.ID.String(),
		Participants: []*dmv1.DMParticipant{}, // Initially empty, populated in loop
		CreatedAt:    timestamppb.New(ch.CreatedAt),
	}
}

func dmMessageToProto(msg *DMMessage) *dmv1.DMMessage {
	proto := &dmv1.DMMessage{
		Id:        strconv.FormatInt(msg.ID, 10),
		ChannelId: msg.ChannelID.String(),
		AuthorId:  msg.AuthorID.String(),
		Content:   msg.Content,
		CreatedAt: timestamppb.New(msg.CreatedAt),
		Deleted:   msg.DeletedAt != nil,
	}

	if msg.EditedAt != nil {
		proto.EditedAt = timestamppb.New(*msg.EditedAt)
	}

	if msg.ReplyToID != nil {
		proto.ReplyToId = strconv.FormatInt(*msg.ReplyToID, 10)
	}

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

	for _, r := range msg.ReadBy {
		proto.ReadBy = append(proto.ReadBy, &dmv1.ReadReceipt{
			UserId: r.UserID.String(),
			ReadAt: timestamppb.New(r.ReadAt),
		})
	}

	return proto
}

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
