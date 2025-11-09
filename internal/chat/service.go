package chat

import (
	"context"
	"fmt"
	"strconv"
	"time"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	repo      *Repository
	hub       *events.Hub
	roomsRepo *rooms.Repository
	cache     *cache.AsidePattern
}

func NewService(repo *Repository, roomsRepo *rooms.Repository, hub *events.Hub, aside *cache.AsidePattern) *Service {
	return &Service{
		repo:      repo,
		hub:       hub,
		roomsRepo: roomsRepo,
		cache:     aside,
	}
}

func (s *Service) isMember(ctx context.Context, roomID, userID uuid.UUID) (bool, error) {
	const ttl = 30 * time.Second
	key := fmt.Sprintf("m:%s:%s", roomID.String(), userID.String())

	loader := func() (interface{}, error) {
		_, err := s.roomsRepo.GetMember(ctx, roomID, userID)
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return nil, err
		}
		return true, nil
	}

	if s.cache != nil {
		v, err := s.cache.GetOrLoad(ctx, key, ttl, loader)
		if err == nil {
			if b, ok := v.(bool); ok {
				return b, nil
			}
		}
	}

	_, err := s.roomsRepo.GetMember(ctx, roomID, userID)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Service) SendMessage(ctx context.Context, roomID, content, replyToID string, attachments []Attachment) (*Message, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}
	authorUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	ok, err := s.isMember(ctx, roomUUID, authorUUID)
	if err != nil {
		return nil, errors.Internal("membership check failed", err)
	}
	if !ok {
		return nil, errors.Forbidden("not a room member")
	}

	msg := &Message{
		RoomID:      roomUUID,
		AuthorID:    authorUUID,
		Content:     content,
		Attachments: attachments,
	}

	if replyToID != "" {
		replyID, err := parseMessageID(replyToID)
		if err != nil {
			return nil, errors.BadRequest("invalid reply_to_id")
		}
		msg.ReplyToID = &replyID
	}

	if err := s.repo.Create(ctx, msg); err != nil {
		return nil, err
	}

	if msg.ReplyToID != nil {
		if err := s.repo.IncrementReplyCount(ctx, *msg.ReplyToID); err != nil {
			return nil, err
		}
	}

	protoAttachments := make([]*commonv1.MessageAttachment, len(msg.Attachments))
	for i, att := range msg.Attachments {
		protoAttachments[i] = &commonv1.MessageAttachment{
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

	if s.hub != nil {
		go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageCreated{
				MessageCreated: &streamv1.MessageCreated{
					Message: &commonv1.Message{
						Id:          strconv.FormatInt(msg.ID, 10),
						RoomId:      msg.RoomID.String(),
						AuthorId:    msg.AuthorID.String(),
						Content:     msg.Content,
						CreatedAt:   timestamppb.New(msg.CreatedAt),
						ReplyToId:   replyToID,
						Attachments: protoAttachments,
					},
				},
			},
		})
	}

	return msg, nil
}

func (s *Service) EditMessage(ctx context.Context, messageID, content string) (*Message, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	id, err := parseMessageID(messageID)
	if err != nil {
		return nil, errors.BadRequest("invalid message id")
	}

	msg, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if msg.AuthorID.String() != userID {
		return nil, errors.Forbidden("can only edit own messages")
	}

	msg.Content = content
	if err := s.repo.Update(ctx, msg); err != nil {
		return nil, err
	}

	go s.hub.BroadcastToRoom(msg.RoomID.String(), &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageEdited{
			MessageEdited: &streamv1.MessageEdited{
				Message: &commonv1.Message{
					Id:        messageID,
					RoomId:    msg.RoomID.String(),
					AuthorId:  msg.AuthorID.String(),
					Content:   msg.Content,
					CreatedAt: timestamppb.New(msg.CreatedAt),
					EditedAt:  timestamppb.New(*msg.EditedAt),
				},
			},
		},
	})

	return msg, nil
}

func (s *Service) DeleteMessage(ctx context.Context, messageID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	id, err := parseMessageID(messageID)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	msg, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if msg.AuthorID.String() != userID {
		return errors.Forbidden("can only delete own messages")
	}

	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return err
	}

	go s.hub.BroadcastToRoom(msg.RoomID.String(), &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageDeleted{
			MessageDeleted: &streamv1.MessageDeleted{
				MessageId: messageID,
				RoomId:    msg.RoomID.String(),
			},
		},
	})

	return nil
}

func (s *Service) ListMessages(ctx context.Context, roomID string, beforeID, afterID *string, limit int) ([]*Message, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	var beforeIDInt, afterIDInt *int64
	if beforeID != nil {
		id, err := parseMessageID(*beforeID)
		if err != nil {
			return nil, errors.BadRequest("invalid before_id")
		}
		beforeIDInt = &id
	}
	if afterID != nil {
		id, err := parseMessageID(*afterID)
		if err != nil {
			return nil, errors.BadRequest("invalid after_id")
		}
		afterIDInt = &id
	}

	return s.repo.ListByRoom(ctx, roomUUID, beforeIDInt, afterIDInt, limit)
}

func (s *Service) AddReaction(ctx context.Context, messageID, emoji string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	msgID, err := parseMessageID(messageID)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	msg, err := s.repo.GetByID(ctx, msgID)
	if err != nil {
		return err
	}

	reaction, err := s.repo.AddReaction(ctx, msgID, userUUID, emoji)
	if err != nil {
		return err
	}

	go s.hub.BroadcastToRoom(msg.RoomID.String(), &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageReactionAdded{
			MessageReactionAdded: &streamv1.MessageReactionAdded{
				MessageId: messageID,
				RoomId:    msg.RoomID.String(),
				Reaction: &commonv1.MessageReaction{
					Id:        reaction.ID.String(),
					MessageId: messageID,
					UserId:    userID,
					Emoji:     emoji,
					CreatedAt: timestamppb.New(reaction.CreatedAt),
				},
			},
		},
	})

	return nil
}

func (s *Service) RemoveReaction(ctx context.Context, messageID, emoji string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	msgID, err := parseMessageID(messageID)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	msg, err := s.repo.GetByID(ctx, msgID)
	if err != nil {
		return err
	}

	reactionID, err := s.repo.RemoveReaction(ctx, msgID, userUUID, emoji)
	if err != nil {
		return err
	}

	go s.hub.BroadcastToRoom(msg.RoomID.String(), &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageReactionRemoved{
			MessageReactionRemoved: &streamv1.MessageReactionRemoved{
				MessageId:  messageID,
				RoomId:     msg.RoomID.String(),
				ReactionId: reactionID.String(),
				UserId:     userID,
			},
		},
	})

	return nil
}

func (s *Service) PinMessage(ctx context.Context, roomID, messageID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	msgID, err := parseMessageID(messageID)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	if err := s.repo.PinMessage(ctx, roomUUID, msgID, userUUID); err != nil {
		return err
	}

	go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessagePinned{
			MessagePinned: &streamv1.MessagePinned{
				MessageId: messageID,
				RoomId:    roomID,
				PinnedBy:  userID,
			},
		},
	})

	return nil
}

func (s *Service) UnpinMessage(ctx context.Context, roomID, messageID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	msgID, err := parseMessageID(messageID)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	if err := s.repo.UnpinMessage(ctx, roomUUID, msgID); err != nil {
		return err
	}

	go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageUnpinned{
			MessageUnpinned: &streamv1.MessageUnpinned{
				MessageId: messageID,
				RoomId:    roomID,
			},
		},
	})

	return nil
}

func (s *Service) ListPinnedMessages(ctx context.Context, roomID string) ([]*Message, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	return s.repo.ListPinnedMessages(ctx, roomUUID)
}

func (s *Service) GetThread(ctx context.Context, parentMessageID string, limit int, cursor string) ([]*Message, string, error) {
	parentID, err := parseMessageID(parentMessageID)
	if err != nil {
		return nil, "", errors.BadRequest("invalid message id")
	}

	var offset int64
	if cursor != "" {
		if _, err := strconv.ParseInt(cursor, 10, 64); err != nil {
			return nil, "", errors.BadRequest("invalid cursor")
		}
		offset, _ = strconv.ParseInt(cursor, 10, 64)
	}

	messages, err := s.repo.GetThreadReplies(ctx, parentID, limit+1, int(offset))
	if err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(messages) > limit {
		messages = messages[:limit]
		nextCursor = strconv.FormatInt(offset+int64(limit), 10)
	}

	return messages, nextCursor, nil
}

func parseMessageID(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}
