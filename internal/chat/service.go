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

const (
	MaxMessageContentLength  = 10000
	MaxAttachmentsPerMessage = 10
	membershipCacheTTL       = 30 * time.Second
	messageCacheTTL          = 5 * time.Minute
)

func NewService(repo *Repository, roomsRepo *rooms.Repository, hub *events.Hub, aside *cache.AsidePattern) *Service {
	return &Service{
		repo:      repo,
		hub:       hub,
		roomsRepo: roomsRepo,
		cache:     aside,
	}
}

func (s *Service) isMember(ctx context.Context, roomID, userID uuid.UUID) (bool, error) {
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
		v, err := s.cache.GetOrLoad(ctx, key, membershipCacheTTL, loader)
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

func (s *Service) SendMessage(ctx context.Context, roomID, content string, replyToID *int64, mentionIDs []uuid.UUID) (*Message, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	if len(content) > MaxMessageContentLength {
		return nil, errors.BadRequest("message content too large")
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
		RoomID:   roomUUID,
		AuthorID: authorUUID,
		Content:  content,
	}

	if replyToID != nil {
		msg.ReplyToID = replyToID
	}

	if err := s.repo.Create(ctx, msg); err != nil {
		return nil, errors.Internal("failed to create message", err)
	}

	if len(mentionIDs) > 0 {
		if err := s.repo.CreateMentions(ctx, msg.ID, mentionIDs); err != nil {
			return nil, errors.Internal("failed to create mentions", err)
		}
		msg.Mentions = mentionIDs
	}

	if msg.ReplyToID != nil {
		if err := s.repo.IncrementReplyCount(ctx, *msg.ReplyToID); err != nil {
			return nil, errors.Internal("failed to increment reply count", err)
		}
	}

	mentionStrings := make([]string, len(mentionIDs))
	for i, id := range mentionIDs {
		mentionStrings[i] = id.String()
	}

	var replyToIDStr string
	if replyToID != nil {
		replyToIDStr = strconv.FormatInt(*replyToID, 10)
	}

	if s.hub != nil {
		event := &streamv1.ServerEvent{
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
						ReplyToId: replyToIDStr,
						Mentions:  mentionStrings,
					},
				},
			},
		}

		go s.hub.BroadcastToRoom(roomID, event)

		if len(mentionIDs) > 0 {
			s.broadcastMentionNotifications(ctx, msg, mentionIDs, authorUUID)
		}
	}

	return msg, nil
}

func (s *Service) broadcastMentionNotifications(ctx context.Context, msg *Message, mentionIDs []uuid.UUID, authorID uuid.UUID) {
	if s.hub == nil {
		return
	}

	for _, mentionedUserID := range mentionIDs {
		if mentionedUserID == authorID {
			continue
		}

		s.hub.BroadcastToUser(mentionedUserID.String(), &streamv1.ServerEvent{
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
						Mentions:  []string{mentionedUserID.String()},
					},
				},
			},
		})
	}
}

func (s *Service) GetMessage(ctx context.Context, roomID string, messageID int64) (*Message, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	ok, err := s.isMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, errors.Internal("membership check failed", err)
	}
	if !ok {
		return nil, errors.Forbidden("not a room member")
	}

	cacheKey := fmt.Sprintf("msg:%d", messageID)

	if s.cache != nil {
		loader := func() (interface{}, error) {
			return s.repo.GetByID(ctx, messageID)
		}

		v, err := s.cache.GetOrLoad(ctx, cacheKey, messageCacheTTL, loader)
		if err == nil {
			if msg, ok := v.(*Message); ok {
				if msg.RoomID != roomUUID {
					return nil, errors.NotFound("message not found in this room")
				}
				return msg, nil
			}
		}
	}

	msg, err := s.repo.GetByID(ctx, messageID)
	if err != nil {
		return nil, err
	}

	if msg.RoomID != roomUUID {
		return nil, errors.NotFound("message not found in this room")
	}

	return msg, nil
}

func (s *Service) EditMessage(ctx context.Context, roomID string, messageID int64, content string) (*Message, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	msg, err := s.repo.GetByID(ctx, messageID)
	if err != nil {
		return nil, err
	}

	if msg.RoomID.String() != roomID {
		return nil, errors.NotFound("message not found in this room")
	}

	if msg.AuthorID.String() != userID {
		return nil, errors.Forbidden("can only edit own messages")
	}

	msg.Content = content
	if err := s.repo.Update(ctx, msg); err != nil {
		return nil, err
	}

	if s.cache != nil {
		cacheKey := fmt.Sprintf("msg:%d", messageID)
		_ = s.cache.Invalidate(ctx, cacheKey)
	}

	if s.hub != nil {
		go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageEdited{
				MessageEdited: &streamv1.MessageEdited{
					Message: &commonv1.Message{
						Id:        strconv.FormatInt(messageID, 10),
						RoomId:    msg.RoomID.String(),
						AuthorId:  msg.AuthorID.String(),
						Content:   msg.Content,
						CreatedAt: timestamppb.New(msg.CreatedAt),
						EditedAt:  timestamppb.New(*msg.EditedAt),
					},
				},
			},
		})
	}

	return msg, nil
}

func (s *Service) DeleteMessage(ctx context.Context, roomID string, messageID int64) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	msg, err := s.repo.GetByID(ctx, messageID)
	if err != nil {
		return err
	}

	if msg.RoomID.String() != roomID {
		return errors.NotFound("message not found in this room")
	}

	if msg.AuthorID.String() != userID {
		return errors.Forbidden("can only delete own messages")
	}

	if err := s.repo.SoftDelete(ctx, messageID); err != nil {
		return err
	}

	if s.cache != nil {
		cacheKey := fmt.Sprintf("msg:%d", messageID)
		_ = s.cache.Invalidate(ctx, cacheKey)
	}

	if s.hub != nil {
		go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageDeleted{
				MessageDeleted: &streamv1.MessageDeleted{
					MessageId: strconv.FormatInt(messageID, 10),
					RoomId:    roomID,
				},
			},
		})
	}

	return nil
}

func (s *Service) ListMessages(ctx context.Context, roomID string, beforeID, afterID *int64, limit int) ([]*Message, bool, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, false, errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, false, errors.BadRequest("invalid room id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, false, errors.BadRequest("invalid user id")
	}

	ok, err := s.isMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, false, errors.Internal("membership check failed", err)
	}
	if !ok {
		return nil, false, errors.Forbidden("not a room member")
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	messages, err := s.repo.ListByRoom(ctx, roomUUID, beforeID, afterID, limit+1)
	if err != nil {
		return nil, false, err
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}

	return messages, hasMore, nil
}

func (s *Service) AddReaction(ctx context.Context, roomID string, messageID int64, emoji string) (*Reaction, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	msg, err := s.repo.GetByID(ctx, messageID)
	if err != nil {
		return nil, err
	}

	if msg.RoomID.String() != roomID {
		return nil, errors.NotFound("message not found in this room")
	}

	reaction, err := s.repo.AddReaction(ctx, messageID, userUUID, emoji)
	if err != nil {
		return nil, err
	}

	if s.cache != nil {
		cacheKey := fmt.Sprintf("msg:%d", messageID)
		_ = s.cache.Invalidate(ctx, cacheKey)
	}

	if s.hub != nil {
		go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageReactionAdded{
				MessageReactionAdded: &streamv1.MessageReactionAdded{
					MessageId: strconv.FormatInt(messageID, 10),
					RoomId:    roomID,
					Reaction: &commonv1.MessageReaction{
						Id:        reaction.ID.String(),
						MessageId: strconv.FormatInt(messageID, 10),
						UserId:    userID,
						Emoji:     emoji,
						CreatedAt: timestamppb.New(reaction.CreatedAt),
					},
				},
			},
		})
	}

	return reaction, nil
}

func (s *Service) RemoveReaction(ctx context.Context, roomID string, messageID int64, emoji string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	msg, err := s.repo.GetByID(ctx, messageID)
	if err != nil {
		return err
	}

	if msg.RoomID.String() != roomID {
		return errors.NotFound("message not found in this room")
	}

	reactionID, err := s.repo.RemoveReaction(ctx, messageID, userUUID, emoji)
	if err != nil {
		return err
	}

	if s.cache != nil {
		cacheKey := fmt.Sprintf("msg:%d", messageID)
		_ = s.cache.Invalidate(ctx, cacheKey)
	}

	if s.hub != nil {
		go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageReactionRemoved{
				MessageReactionRemoved: &streamv1.MessageReactionRemoved{
					MessageId:  strconv.FormatInt(messageID, 10),
					RoomId:     roomID,
					ReactionId: reactionID.String(),
					UserId:     userID,
				},
			},
		})
	}

	return nil
}

func (s *Service) PinMessage(ctx context.Context, roomID string, messageID int64) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	msg, err := s.repo.GetByID(ctx, messageID)
	if err != nil {
		return err
	}

	if msg.RoomID != roomUUID {
		return errors.NotFound("message not found in this room")
	}

	if err := s.repo.PinMessage(ctx, roomUUID, messageID, userUUID); err != nil {
		return err
	}

	if s.cache != nil {
		cacheKey := fmt.Sprintf("msg:%d", messageID)
		_ = s.cache.Invalidate(ctx, cacheKey)
	}

	if s.hub != nil {
		go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessagePinned{
				MessagePinned: &streamv1.MessagePinned{
					MessageId: strconv.FormatInt(messageID, 10),
					RoomId:    roomID,
					PinnedBy:  userID,
				},
			},
		})
	}

	return nil
}

func (s *Service) UnpinMessage(ctx context.Context, roomID string, messageID int64) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	msg, err := s.repo.GetByID(ctx, messageID)
	if err != nil {
		return err
	}

	if msg.RoomID != roomUUID {
		return errors.NotFound("message not found in this room")
	}

	if err := s.repo.UnpinMessage(ctx, roomUUID, messageID); err != nil {
		return err
	}

	if s.cache != nil {
		cacheKey := fmt.Sprintf("msg:%d", messageID)
		_ = s.cache.Invalidate(ctx, cacheKey)
	}

	if s.hub != nil {
		go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageUnpinned{
				MessageUnpinned: &streamv1.MessageUnpinned{
					MessageId: strconv.FormatInt(messageID, 10),
					RoomId:    roomID,
				},
			},
		})
	}

	return nil
}

func (s *Service) ListPinnedMessages(ctx context.Context, roomID string) ([]*Message, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	ok, err := s.isMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, errors.Internal("membership check failed", err)
	}
	if !ok {
		return nil, errors.Forbidden("not a room member")
	}

	return s.repo.ListPinnedMessages(ctx, roomUUID)
}

func (s *Service) GetThread(ctx context.Context, roomID string, parentMessageID int64, beforeID *int64, limit int) (*Message, []*Message, bool, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, nil, false, errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, nil, false, errors.BadRequest("invalid room id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, nil, false, errors.BadRequest("invalid user id")
	}

	ok, err := s.isMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, nil, false, errors.Internal("membership check failed", err)
	}
	if !ok {
		return nil, nil, false, errors.Forbidden("not a room member")
	}

	parent, err := s.repo.GetByID(ctx, parentMessageID)
	if err != nil {
		return nil, nil, false, err
	}

	if parent.RoomID != roomUUID {
		return nil, nil, false, errors.NotFound("message not found in this room")
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var offset int
	if beforeID != nil {
		offset = int(*beforeID)
	}

	replies, err := s.repo.GetThreadReplies(ctx, parentMessageID, limit+1, offset)
	if err != nil {
		return nil, nil, false, err
	}

	hasMore := len(replies) > limit
	if hasMore {
		replies = replies[:limit]
	}

	return parent, replies, hasMore, nil
}

func (s *Service) SearchMessages(ctx context.Context, roomID, query string, beforeID *int64, limit int) ([]*Message, bool, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, false, errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, false, errors.BadRequest("invalid room id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, false, errors.BadRequest("invalid user id")
	}

	ok, err := s.isMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, false, errors.Internal("membership check failed", err)
	}
	if !ok {
		return nil, false, errors.Forbidden("not a room member")
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	messages, err := s.repo.Search(ctx, roomUUID, query, limit+1)
	if err != nil {
		return nil, false, err
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}

	return messages, hasMore, nil
}
