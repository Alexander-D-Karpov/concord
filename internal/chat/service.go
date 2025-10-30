package chat

import (
	"context"
	"strconv"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/messages"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	repo *messages.Repository
	hub  *events.Hub
}

func NewService(repo *messages.Repository, hub *events.Hub) *Service {
	return &Service{
		repo: repo,
		hub:  hub,
	}
}

func (s *Service) SendMessage(ctx context.Context, roomID, content, replyToID string) (*messages.Message, error) {
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

	msg := &messages.Message{
		RoomID:   roomUUID,
		AuthorID: authorUUID,
		Content:  content,
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

	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageCreated{
			MessageCreated: &streamv1.MessageCreated{
				Message: &commonv1.Message{
					Id:        strconv.FormatInt(msg.ID, 10),
					RoomId:    msg.RoomID.String(),
					AuthorId:  msg.AuthorID.String(),
					Content:   msg.Content,
					CreatedAt: timestamppb.New(msg.CreatedAt),
					ReplyToId: replyToID,
				},
			},
		},
	})

	return msg, nil
}

func (s *Service) EditMessage(ctx context.Context, messageID, content string) (*messages.Message, error) {
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

	s.hub.BroadcastToRoom(msg.RoomID.String(), &streamv1.ServerEvent{
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

	s.hub.BroadcastToRoom(msg.RoomID.String(), &streamv1.ServerEvent{
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

func (s *Service) ListMessages(ctx context.Context, roomID string, beforeID, afterID *string, limit int) ([]*messages.Message, error) {
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

	s.hub.BroadcastToRoom(msg.RoomID.String(), &streamv1.ServerEvent{
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

	s.hub.BroadcastToRoom(msg.RoomID.String(), &streamv1.ServerEvent{
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

	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
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

	s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
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

func (s *Service) ListPinnedMessages(ctx context.Context, roomID string) ([]*messages.Message, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	return s.repo.ListPinnedMessages(ctx, roomUUID)
}

func (s *Service) GetThread(ctx context.Context, parentMessageID string, limit int, cursor string) ([]*messages.Message, string, error) {
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

type Message = messages.Message
