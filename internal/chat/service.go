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
	"github.com/Alexander-D-Karpov/concord/internal/messaging/editing"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/mentions"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/slowmode"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MessagePusher notifies a room member of a new message (nil-safe; injected).
type MessagePusher interface {
	PushRoomMessage(ctx context.Context, userID, roomID uuid.UUID, messageID int64, senderID uuid.UUID)
}

// Service holds the room-messaging business logic: it enforces membership and
// slow mode, coordinates the repository with mention parsing and edit-history
// recording, caches membership/messages/pins, and broadcasts stream events via
// the hub. cache may be nil, in which case every path falls back to the repo.
type Service struct {
	repo       *Repository
	hub        *events.Hub
	roomsRepo  *rooms.Repository
	cache      *cache.AsidePattern
	slowmode   *slowmode.Service
	mentions   *mentions.Parser
	recorder   *editing.Recorder
	editReader *editing.Reader
	push       MessagePusher
}

// SetPusher installs the push notifier used to notify room members of new messages.
func (s *Service) SetPusher(p MessagePusher) { s.push = p }

// NewService assembles the room-messaging Service from its collaborators. A nil
// aside disables caching; a nil slowmode, mentions, hub, or editReader disables
// the corresponding feature at call time.
func NewService(repo *Repository, roomsRepo *rooms.Repository, hub *events.Hub, aside *cache.AsidePattern, sm *slowmode.Service, mp *mentions.Parser, rec *editing.Recorder, er *editing.Reader) *Service {
	return &Service{
		repo:       repo,
		hub:        hub,
		roomsRepo:  roomsRepo,
		cache:      aside,
		slowmode:   sm,
		mentions:   mp,
		recorder:   rec,
		editReader: er,
	}
}

// SendMessageParams is the input to SendMessage. Beyond RoomID/Content it
// carries optional reply, mention-hint, attachment, forward, and media-group
// data; the Forward* fields together describe a forwarded message's origin.
type SendMessageParams struct {
	RoomID  string
	Content string

	ReplyToID   *int64
	MentionIDs  []uuid.UUID
	Attachments []Attachment

	ForwardFromUserID   *uuid.UUID
	ForwardFromUserName *string
	ForwardFromRoomID   *uuid.UUID
	ForwardFromMsgID    *int64
	ForwardOriginalTS   *time.Time

	MediaGroupID       *string
	ReplyQuotedContent *string
	ReplyMentionAuthor bool
}

const (
	// MaxMessageContentLength is the upper bound on message text; longer content
	// is rejected with BadRequest.
	MaxMessageContentLength = 10000
	// MaxAttachmentsPerMessage caps attachments per message (also mirrored into
	// the repository's TableSpec).
	MaxAttachmentsPerMessage = 10
	// membershipCacheTTL is how long a room-membership result is cached.
	membershipCacheTTL = 30 * time.Second
	// messageCacheTTL is how long a single fetched message is cached.
	messageCacheTTL = 5 * time.Minute
	// pinnedTTL is how long a room's pinned-message list is cached.
	pinnedTTL = 10 * time.Minute
)

// isMember reports whether userID belongs to roomID, caching the boolean under
// "m:<room>:<user>" for membershipCacheTTL. A missing membership is cached as
// false (not an error); on any cache failure it falls through to a direct repo
// lookup so correctness never depends on the cache.
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

// SendMessage validates and persists a new room message, then fans out events.
// It requires an authenticated caller who is a room member, enforces the content
// and attachment size limits, and applies slow mode via slowmode.CheckAndStamp
// (returning BadRequest with the remaining wait when the user is rate-limited).
// After insert it resolves mentions (parsing content plus the caller's hints),
// persists mention rows, bumps the parent reply count for replies, then
// asynchronously broadcasts MessageCreated to the room and per-user mention
// notifications. Broadcasts run in a goroutine, so a returned message does not
// guarantee delivery.
func (s *Service) SendMessage(ctx context.Context, params SendMessageParams) (*Message, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	if len(params.Content) > MaxMessageContentLength {
		return nil, errors.BadRequest("message content too large")
	}
	if len(params.Attachments) > MaxAttachmentsPerMessage {
		return nil, errors.BadRequest("too many attachments")
	}

	roomUUID, err := uuid.Parse(params.RoomID)
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

	// Load room settings and the author's role once; both feed the post policy,
	// slow mode, and word filtering below.
	settings, err := s.roomsRepo.GetSettings(ctx, roomUUID)
	if err != nil {
		return nil, errors.Internal("failed to load room settings", err)
	}
	role := "member"
	if member, err := s.roomsRepo.GetMember(ctx, roomUUID, authorUUID); err == nil && member != nil {
		role = member.Role
	} else if err != nil && !errors.IsNotFound(err) {
		return nil, errors.Internal("failed to get member", err)
	}

	if settings.WhoCanPost == "moderator" && role != "moderator" && role != "admin" {
		return nil, errors.Forbidden("only moderators can post in this room")
	}

	// Mask any configured filter words before the message is persisted or parsed.
	if len(settings.WordFilters) > 0 {
		params.Content = censorWords(params.Content, settings.WordFilters)
	}

	if s.slowmode != nil {
		if remaining, err := s.slowmode.CheckAndStamp(ctx, roomUUID, authorUUID, role); err != nil {
			return nil, errors.Internal("slowmode check failed", err)
		} else if remaining > 0 {
			return nil, errors.BadRequest(fmt.Sprintf("slow mode: wait %d seconds", remaining))
		}
	}

	msg := &Message{
		RoomID:              roomUUID,
		AuthorID:            authorUUID,
		Content:             params.Content,
		Attachments:         params.Attachments,
		ReplyToID:           params.ReplyToID,
		ForwardFromUserID:   params.ForwardFromUserID,
		ForwardFromUserName: params.ForwardFromUserName,
		ForwardFromRoomID:   params.ForwardFromRoomID,
		ForwardFromMsgID:    params.ForwardFromMsgID,
		ForwardOriginalTS:   params.ForwardOriginalTS,
		MediaGroupID:        params.MediaGroupID,
		ReplyQuotedContent:  params.ReplyQuotedContent,
		ReplyMentionAuthor:  params.ReplyMentionAuthor,
	}

	if err := s.repo.Create(ctx, msg); err != nil {
		return nil, errors.Internal("failed to create message", err)
	}

	resolved := params.MentionIDs
	if s.mentions != nil {
		r, err := s.mentions.Parse(ctx, params.Content, params.MentionIDs)
		if err != nil {
			s.hub.Logger().Warn("mention parse failed", zap.Error(err))
		} else {
			resolved = r
		}
	}
	if len(resolved) > 0 {
		if err := s.repo.CreateMentions(ctx, msg.ID, resolved); err != nil {
			return nil, errors.Internal("failed to create mentions", err)
		}
		msg.Mentions = resolved
	}

	if msg.ReplyToID != nil {
		if err := s.repo.IncrementReplyCount(ctx, *msg.ReplyToID); err != nil {
			return nil, errors.Internal("failed to increment reply count", err)
		}
	}

	if s.hub != nil {
		go s.hub.BroadcastToRoom(params.RoomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageCreated{
				MessageCreated: &streamv1.MessageCreated{Message: toProtoMessage(msg)},
			},
		})
		if len(resolved) > 0 {
			s.broadcastMentionNotifications(ctx, msg, resolved, authorUUID)
		}
	}

	if s.push != nil {
		if members, err := s.roomsRepo.ListMembers(ctx, roomUUID); err == nil {
			for _, mem := range members {
				if mem.UserID == authorUUID {
					continue
				}
				s.push.PushRoomMessage(ctx, mem.UserID, roomUUID, msg.ID, authorUUID)
			}
		}
	}

	return msg, nil
}

// broadcastMentionNotifications sends a per-user MessageCreated event to each
// mentioned user (with Mentions narrowed to just that user), skipping the author
// so self-mentions don't notify. It is a no-op when no hub is configured.
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

// GetMessage returns a single message after verifying the caller is a member of
// roomID. It serves from the "msg:<id>" cache when present, but a cached message
// whose RoomID differs from roomID is treated as NotFound so cross-room reads
// leak nothing; on a miss it loads from the repo and back-fills the cache.
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
		var cached Message
		err := s.cache.Get(ctx, cacheKey, &cached)
		if err == nil {
			if cached.RoomID != roomUUID {
				return nil, errors.NotFound("message not found in this room")
			}
			return &cached, nil
		}
		if err != nil && err != cache.ErrCacheMiss {
			return nil, errors.Internal("cache read failed", err)
		}
	}

	msg, err := s.repo.GetByID(ctx, messageID)
	if err != nil {
		return nil, err
	}

	if msg.RoomID != roomUUID {
		return nil, errors.NotFound("message not found in this room")
	}

	if s.cache != nil {
		_ = s.cache.Set(ctx, cacheKey, msg, messageCacheTTL)
	}

	return msg, nil
}

// EditMessage replaces a message's content. Only the original author may edit
// (Forbidden otherwise), and the message must belong to roomID (NotFound
// otherwise). On success it invalidates the message cache and asynchronously
// broadcasts MessageEdited to the room.
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

	// Apply the room's word filter to edited content too, so an edit cannot bypass it.
	settings, err := s.roomsRepo.GetSettings(ctx, msg.RoomID)
	if err != nil {
		return nil, errors.Internal("failed to load room settings", err)
	}
	content = censorWords(content, settings.WordFilters)

	msg.Content = content
	if err := s.repo.Update(ctx, msg, s.recorder); err != nil {
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

// DeleteMessage soft-deletes a message. Only the author may delete it and it
// must belong to roomID; on success it invalidates the cache and asynchronously
// broadcasts MessageDeleted to the room.
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

// ListMessages returns a page of a room's messages for a member of that room.
// It over-fetches by one (limit+1) to compute the hasMore flag, trimming the
// extra element before returning.
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

// AddReaction adds the caller's emoji reaction to a message in roomID. It
// verifies the message belongs to the room, invalidates the message cache, and
// asynchronously broadcasts MessageReactionAdded. Note it does not re-check room
// membership here beyond the room-match check.
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

// RemoveReaction removes the caller's emoji reaction from a message in roomID,
// invalidates the message cache, and asynchronously broadcasts
// MessageReactionRemoved with the removed reaction's ID.
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

// PinMessage pins a message in roomID on behalf of the caller (recorded as the
// pinner), verifying the message belongs to the room. On success it invalidates
// the message cache and asynchronously broadcasts MessagePinned.
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

// UnpinMessage removes a room's pin for the message, verifying it belongs to the
// room, invalidates the message cache, and asynchronously broadcasts
// MessageUnpinned.
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

// ListPinnedMessages returns a room's pinned messages for a member of the room,
// read-through cached under "room:pinned:<room>" for pinnedTTL.
func (s *Service) ListPinnedMessages(ctx context.Context, roomID string) ([]*Message, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}
	userUUID, _ := uuid.Parse(userID)
	ok, err := s.isMember(ctx, roomUUID, userUUID)
	if err != nil || !ok {
		if err != nil {
			return nil, errors.Internal("membership check failed", err)
		}
		return nil, errors.Forbidden("not a room member")
	}

	if s.cache != nil {
		key := fmt.Sprintf("room:pinned:%s", roomID)
		var cached []*Message
		if err := s.cache.Get(ctx, key, &cached); err == nil {
			return cached, nil
		}
		msgs, err := s.repo.ListPinnedMessages(ctx, roomUUID)
		if err != nil {
			return nil, err
		}
		_ = s.cache.Set(ctx, key, msgs, pinnedTTL)
		return msgs, nil
	}
	return s.repo.ListPinnedMessages(ctx, roomUUID)
}

// GetThread returns a parent message and a page of its replies for a room
// member. Despite the name, beforeID is used as a numeric OFFSET into the reply
// list, not as a message-ID cursor. It over-fetches by one to compute hasMore.
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

// SearchMessages runs a full-text search within a room for a member of it,
// over-fetching by one to compute hasMore. The beforeID parameter is currently
// unused by the underlying repo search.
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

// GetEditHistory returns the recorded prior versions of a message for a room
// member. It returns Internal when no edit reader is configured and NotFound
// when the message does not belong to roomID.
func (s *Service) GetEditHistory(ctx context.Context, roomID string, messageID int64) ([]editing.Entry, error) {
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

	msg, err := s.repo.GetByID(ctx, messageID)
	if err != nil {
		return nil, err
	}
	if msg.RoomID != roomUUID {
		return nil, errors.NotFound("message not found in this room")
	}
	if s.editReader == nil {
		return nil, errors.Internal("edit history not available", nil)
	}
	return s.editReader.ListRoom(ctx, messageID)
}
