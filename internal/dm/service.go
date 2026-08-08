package dm

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
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service holds DM business logic: participant checks, message CRUD, DM voice
// calls, and event fan-out. It spans both repositories (repo for channels/calls,
// msgRepo for messages) and, unlike room chat, broadcasts by delivering events to
// each of the channel's two users individually rather than to a room. cache may
// be nil, disabling the channel/participant caches.
// MessagePusher notifies a DM participant of a new message, or rings the
// callee for an incoming call (nil-safe; injected).
type MessagePusher interface {
	PushDMMessage(ctx context.Context, userID, channelID uuid.UUID, messageID int64, senderID uuid.UUID)
	PushCall(userID uuid.UUID, callID, roomOrDMID, callerID string)
}

type Service struct {
	repo        *Repository
	msgRepo     *MessageRepository
	usersRepo   *users.Repository
	hub         *events.Hub
	voiceAssign *voiceassign.Service
	presence    *users.PresenceManager
	mentions    *mentions.Parser
	editReader  *editing.Reader
	cache       *cache.AsidePattern
	logger      *zap.Logger
	push        MessagePusher
}

// SetPusher installs the push notifier used to notify the other participant of new messages.
func (s *Service) SetPusher(p MessagePusher) { s.push = p }

// NewService assembles the DM Service from its collaborators. A nil aside
// disables caching; presence and mentions may also be nil, degrading gracefully.
func NewService(
	repo *Repository,
	msgRepo *MessageRepository,
	usersRepo *users.Repository,
	hub *events.Hub,
	voiceAssign *voiceassign.Service,
	presence *users.PresenceManager,
	mentionParser *mentions.Parser,
	editReader *editing.Reader,
	aside *cache.AsidePattern,
	logger *zap.Logger,
) *Service {
	return &Service{
		repo:        repo,
		msgRepo:     msgRepo,
		usersRepo:   usersRepo,
		hub:         hub,
		voiceAssign: voiceAssign,
		presence:    presence,
		mentions:    mentionParser,
		editReader:  editReader,
		cache:       aside,
		logger:      logger,
	}
}

const (
	// dmParticipantTTL is how long a participation check is cached.
	dmParticipantTTL = 60 * time.Second
)

// isParticipant reports whether userID belongs to a channel, read-through cached
// under "dm:p:<channel>:<user>" for dmParticipantTTL. It falls back to a direct
// repo lookup with no cache or on any cache error.
func (s *Service) isParticipant(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	if s.cache == nil {
		return s.repo.IsParticipant(ctx, channelID, userID)
	}
	key := fmt.Sprintf("dm:p:%s:%s", channelID, userID)
	loader := func() (interface{}, error) {
		return s.repo.IsParticipant(ctx, channelID, userID)
	}
	v, err := s.cache.GetOrLoad(ctx, key, dmParticipantTTL, loader)
	if err == nil {
		if b, ok := v.(bool); ok {
			return b, nil
		}
	}
	return s.repo.IsParticipant(ctx, channelID, userID)
}

// invalidateChannel drops the cached "dm:ch:<channel>" entry so the next
// getChannel reloads fresh state (e.g. after a message or call change updates
// updated_at/HasActiveCall). No-op without a cache.
func (s *Service) invalidateChannel(ctx context.Context, channelID uuid.UUID) {
	if s.cache == nil {
		return
	}
	_ = s.cache.Invalidate(ctx, fmt.Sprintf("dm:ch:%s", channelID))
}

// decorateChannelStatuses overwrites each channel's OtherUserStatus with the
// effective presence, combining the peer's stored status preference with live
// presence from the presence manager (defaulting to offline when unavailable).
func (s *Service) decorateChannelStatuses(channels []*DMChannelWithUser) {
	for _, ch := range channels {
		presence := users.StatusOffline
		if s.presence != nil {
			presence = s.presence.GetStatus(ch.OtherUserID)
		}

		ch.OtherUserStatus = users.EffectiveStatus(
			users.NormalizeStatusPreference(ch.OtherUserStatus),
			presence,
		)
	}
}

// GetOrCreateDM returns (creating if needed) the DM channel between the caller
// and otherUserID. It rejects self-DMs (BadRequest) and requires the other user
// to exist (NotFound), relying on the repo's canonical pairing to dedupe.
func (s *Service) GetOrCreateDM(ctx context.Context, otherUserID string) (*DMChannel, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	user1UUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	user2UUID, err := uuid.Parse(otherUserID)
	if err != nil {
		return nil, errors.BadRequest("invalid other user id")
	}

	if user1UUID == user2UUID {
		return nil, errors.BadRequest("cannot create DM with yourself")
	}

	_, err = s.usersRepo.GetByID(ctx, user2UUID)
	if err != nil {
		return nil, errors.NotFound("user not found")
	}

	channel, err := s.repo.GetOrCreate(ctx, user1UUID, user2UUID)
	if err != nil {
		return nil, errors.Internal("failed to create DM channel", err)
	}

	return channel, nil
}

// ListDMs returns the caller's DM channels with each peer's profile and live
// presence applied via decorateChannelStatuses.
func (s *Service) ListDMs(ctx context.Context) ([]*DMChannelWithUser, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	channels, err := s.repo.ListByUser(ctx, userUUID)
	if err != nil {
		return nil, err
	}

	s.decorateChannelStatuses(channels)
	return channels, nil
}

// CloseDM deletes a DM channel on behalf of a participant. Only a participant may
// close it (Forbidden otherwise); this is a hard delete of the channel row.
func (s *Service) CloseDM(ctx context.Context, channelID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	channelUUID, err := uuid.Parse(channelID)
	if err != nil {
		return errors.BadRequest("invalid channel id")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	isParticipant, err := s.repo.IsParticipant(ctx, channelUUID, userUUID)
	if err != nil {
		return errors.Internal("failed to check participation", err)
	}

	if !isParticipant {
		return errors.Forbidden("not a participant of this DM")
	}

	return s.repo.Delete(ctx, channelUUID)
}

// GetChannel loads a channel by id string. It performs no participant check, so
// callers that need authorization must gate access themselves.
func (s *Service) GetChannel(ctx context.Context, channelID string) (*DMChannel, error) {
	channelUUID, err := uuid.Parse(channelID)
	if err != nil {
		return nil, errors.BadRequest("invalid channel id")
	}

	return s.repo.GetByID(ctx, channelUUID)
}

// checkParticipant is the shared authorization gate for DM operations: it
// requires an authenticated caller and valid channel id, verifies the caller is
// a participant (Forbidden otherwise), and returns the parsed channel and user
// UUIDs for reuse.
func (s *Service) checkParticipant(ctx context.Context, channelID string) (uuid.UUID, uuid.UUID, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return uuid.Nil, uuid.Nil, errors.Unauthorized("user not authenticated")
	}
	channelUUID, err := uuid.Parse(channelID)
	if err != nil {
		return uuid.Nil, uuid.Nil, errors.BadRequest("invalid channel id")
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return uuid.Nil, uuid.Nil, errors.BadRequest("invalid user id")
	}

	isParticipant, err := s.isParticipant(ctx, channelUUID, userUUID)
	if err != nil {
		return uuid.Nil, uuid.Nil, errors.Internal("failed to check participation", err)
	}
	if !isParticipant {
		return uuid.Nil, uuid.Nil, errors.Forbidden("not a participant")
	}
	return channelUUID, userUUID, nil
}

// SendMessage persists a DM message from the caller (a participant), resolving
// mentions from content plus the provided hint ids (falling back to the raw
// hints if parsing fails). After insert it invalidates the channel cache and
// broadcasts DmMessageCreated to both participants.
func (s *Service) SendMessage(ctx context.Context, channelID, content string, replyToID string, attachments []DMAttachment, mentionUserIDs []string) (*DMMessage, error) {
	channelUUID, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, err
	}

	msg := &DMMessage{
		ChannelID:          channelUUID,
		AuthorID:           userUUID,
		Content:            content,
		Attachments:        attachments,
		ReplyMentionAuthor: true,
	}

	if replyToID != "" {
		id, err := strconv.ParseInt(replyToID, 10, 64)
		if err != nil {
			return nil, errors.BadRequest("invalid reply_to_id")
		}
		msg.ReplyToID = &id
	}

	hints := make([]uuid.UUID, 0, len(mentionUserIDs))
	for _, id := range mentionUserIDs {
		if uid, err := uuid.Parse(id); err == nil {
			hints = append(hints, uid)
		}
	}
	if s.mentions != nil {
		resolved, err := s.mentions.Parse(ctx, content, hints)
		if err != nil {
			s.logger.Warn("mention parse failed", zap.Error(err))
			msg.Mentions = hints
		} else {
			msg.Mentions = resolved
		}
	} else {
		msg.Mentions = hints
	}

	if err := s.msgRepo.Create(ctx, msg); err != nil {
		return nil, errors.Internal("failed to create message", err)
	}

	s.invalidateChannel(ctx, channelUUID)
	s.broadcastMessageCreated(ctx, channelUUID, msg)

	if s.push != nil {
		if channel, err := s.repo.GetByID(ctx, channelUUID); err == nil && channel != nil {
			other := channel.User1ID
			if other == userUUID {
				other = channel.User2ID
			}
			s.push.PushDMMessage(ctx, other, channelUUID, msg.ID, userUUID)
		}
	}

	return msg, nil
}

// EditMessage replaces a DM message's content. The caller must be a participant
// and the message's author (Forbidden otherwise); the edit is recorded in
// history via the repo's recorder, then MessageEdited is broadcast to both users.
func (s *Service) EditMessage(ctx context.Context, channelID, messageID, content string) (*DMMessage, error) {
	_, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, err
	}

	msgID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return nil, errors.BadRequest("invalid message id")
	}

	msg, err := s.msgRepo.GetByID(ctx, msgID)
	if err != nil {
		return nil, err
	}

	if msg.AuthorID != userUUID {
		return nil, errors.Forbidden("can only edit own messages")
	}

	msg.Content = content
	if err := s.msgRepo.Update(ctx, msg, s.msgRepo.recorder); err != nil {
		return nil, err
	}

	s.broadcastMessageEdited(ctx, msg)

	return msg, nil
}

// DeleteMessage soft-deletes a DM message. The caller must be the author
// (Forbidden otherwise); on success MessageDeleted is broadcast to both users.
func (s *Service) DeleteMessage(ctx context.Context, channelID, messageID string) error {
	_, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return err
	}

	msgID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	msg, err := s.msgRepo.GetByID(ctx, msgID)
	if err != nil {
		return err
	}

	if msg.AuthorID != userUUID {
		return errors.Forbidden("can only delete own messages")
	}

	if err := s.msgRepo.SoftDelete(ctx, msgID); err != nil {
		return err
	}

	s.broadcastMessageDeleted(ctx, msg.ChannelID, messageID)

	return nil
}

// ListMessages returns a page of a channel's messages for a participant. The
// string before/after cursors are parsed to int64 Snowflakes (BadRequest if
// malformed); it over-fetches by one to compute hasMore.
func (s *Service) ListMessages(ctx context.Context, channelID string, beforeID, afterID *string, limit int) ([]*DMMessage, bool, error) {
	channelUUID, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, false, err
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var beforeIDInt, afterIDInt *int64
	if beforeID != nil {
		id, err := strconv.ParseInt(*beforeID, 10, 64)
		if err != nil {
			return nil, false, errors.BadRequest("invalid before_id")
		}
		beforeIDInt = &id
	}
	if afterID != nil {
		id, err := strconv.ParseInt(*afterID, 10, 64)
		if err != nil {
			return nil, false, errors.BadRequest("invalid after_id")
		}
		afterIDInt = &id
	}

	messages, err := s.msgRepo.ListByChannel(ctx, channelUUID, beforeIDInt, afterIDInt, limit+1)
	if err != nil {
		return nil, false, err
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	return messages, hasMore, nil
}

// AddReaction adds the caller's emoji reaction to a message and broadcasts
// MessageReactionAdded to both participants. Use AddReactionAndReturn when the
// created reaction is needed in the response.
func (s *Service) AddReaction(ctx context.Context, channelID, messageID, emoji string) error {
	_, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return err
	}

	msgID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	msg, err := s.msgRepo.GetByID(ctx, msgID)
	if err != nil {
		return err
	}

	reaction, err := s.msgRepo.AddReaction(ctx, msgID, userUUID, emoji)
	if err != nil {
		return err
	}

	s.broadcastReactionAdded(ctx, msg.ChannelID, messageID, reaction)

	return nil
}

// RemoveReaction removes the caller's emoji reaction and broadcasts
// MessageReactionRemoved (with the removed reaction id) to both participants.
func (s *Service) RemoveReaction(ctx context.Context, channelID, messageID, emoji string) error {
	_, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return err
	}

	msgID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	msg, err := s.msgRepo.GetByID(ctx, msgID)
	if err != nil {
		return err
	}

	reactionID, err := s.msgRepo.RemoveReaction(ctx, msgID, userUUID, emoji)
	if err != nil {
		return err
	}

	s.broadcastReactionRemoved(ctx, msg.ChannelID, messageID, reactionID.String(), userUUID.String())

	return nil
}

// PinMessage pins a message in a channel (recording the caller as pinner) and
// broadcasts MessagePinned to both participants.
func (s *Service) PinMessage(ctx context.Context, channelID, messageID string) error {
	channelUUID, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return err
	}

	msgID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	if err := s.msgRepo.PinMessage(ctx, channelUUID, msgID, userUUID); err != nil {
		return err
	}

	s.broadcastMessagePinned(ctx, channelUUID, messageID, userUUID.String())

	return nil
}

// UnpinMessage removes a channel's pin for a message and broadcasts
// MessageUnpinned to both participants.
func (s *Service) UnpinMessage(ctx context.Context, channelID, messageID string) error {
	channelUUID, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return err
	}

	msgID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return errors.BadRequest("invalid message id")
	}

	if err := s.msgRepo.UnpinMessage(ctx, channelUUID, msgID); err != nil {
		return err
	}

	s.broadcastMessageUnpinned(ctx, channelUUID, messageID)

	return nil
}

// ListPinnedMessages returns a channel's pinned messages for a participant.
func (s *Service) ListPinnedMessages(ctx context.Context, channelID string) ([]*DMMessage, error) {
	channelUUID, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, err
	}

	return s.msgRepo.ListPinnedMessages(ctx, channelUUID)
}

// GetThread returns a page of replies to messageID for a participant. The cursor
// is a numeric OFFSET (not a message-ID cursor); the returned nextCursor is the
// offset to pass for the following page, empty when the last page is reached.
func (s *Service) GetThread(ctx context.Context, channelID, messageID string, limit int, cursor string) ([]*DMMessage, string, error) {
	_, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, "", err
	}

	parentID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return nil, "", errors.BadRequest("invalid message id")
	}

	var offset int64
	if cursor != "" {
		offset, _ = strconv.ParseInt(cursor, 10, 64)
	}

	messages, err := s.msgRepo.GetThreadReplies(ctx, parentID, limit+1, int(offset))
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

// SearchMessages runs a substring content search within a channel for a
// participant; limit is clamped to (0,100] and defaults to 50.
func (s *Service) SearchMessages(ctx context.Context, channelID, query string, limit int) ([]*DMMessage, error) {
	channelUUID, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, err
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	return s.msgRepo.Search(ctx, channelUUID, query, limit)
}

// StartCall begins a DM voice call for a participant: it assigns the caller to a
// voice server, then records the call row. If persisting the call fails (or the
// server returned an unparseable id) it rolls back the voice session via
// LeaveVoice; a Conflict from an already-active call is surfaced as such. On
// success it invalidates the channel cache, notifies the peer via DmCallStarted,
// and returns the assignment plus the new call id.
func (s *Service) StartCall(ctx context.Context, channelID string, audioOnly bool) (*voiceassign.VoiceAssignmentResult, string, error) {
	channelUUID, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, "", err
	}

	assignment, err := s.voiceAssign.AssignToDMCall(ctx, channelID, userUUID.String(), "", audioOnly)
	if err != nil {
		return nil, "", err
	}

	serverUUID, err := uuid.Parse(assignment.ServerID)
	if err != nil {
		_ = s.voiceAssign.LeaveVoice(ctx, channelID, userUUID.String())
		return nil, "", errors.Internal("voice server returned invalid id", err)
	}

	call, err := s.repo.CreateCall(ctx, channelUUID, userUUID, &serverUUID)
	if err != nil {
		// roll back the voice session we just created
		_ = s.voiceAssign.LeaveVoice(ctx, channelID, userUUID.String())
		if errors.IsConflict(err) {
			existing, getErr := s.repo.GetActiveCall(ctx, channelUUID)
			if getErr == nil && existing != nil {
				return nil, "", errors.Conflict("call already active")
			}
		}
		return nil, "", errors.Internal("failed to create call", err)
	}

	s.invalidateChannel(ctx, channelUUID)
	s.broadcastCallStarted(ctx, channelUUID, userUUID, audioOnly)

	// Ring the callee (the other participant, never the caller) now that the
	// call is durably recorded. callID is the DM call row's own id (call.ID),
	// not the channel id, since roomOrDMID already carries the channel and the
	// callID must uniquely identify this particular call/ring.
	if s.push != nil {
		if callee, err := s.GetOtherParticipant(ctx, channelUUID, userUUID); err == nil {
			s.push.PushCall(callee, call.ID.String(), channelID, userUUID.String())
		}
	}

	return assignment, call.ID.String(), nil
}

// JoinCall joins the caller to a channel's call, starting one if none is active
// (delegating to StartCall). It prefers the call's existing voice server, and if
// the assignment lands on a different server it persists the move and invalidates
// the channel cache. It returns the assignment and the other participants (self
// excluded), and notifies the peer via DmCallStarted.
func (s *Service) JoinCall(ctx context.Context, channelID string, audioOnly bool) (*voiceassign.VoiceAssignmentResult, []voiceassign.VoiceParticipant, error) {
	channelUUID, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, nil, err
	}

	call, err := s.repo.GetActiveCall(ctx, channelUUID)
	if err != nil {
		return nil, nil, errors.Internal("failed to check active call", err)
	}

	if call == nil {
		assignment, _, startErr := s.StartCall(ctx, channelID, audioOnly)
		if startErr != nil {
			return nil, nil, startErr
		}
		return assignment, []voiceassign.VoiceParticipant{}, nil
	}

	preferredServerID := ""
	if call.VoiceServerID != nil {
		preferredServerID = call.VoiceServerID.String()
	}

	assignment, err := s.voiceAssign.AssignToDMCallOnServer(ctx, channelID, userUUID.String(), preferredServerID, "", audioOnly)
	if err != nil {
		return nil, nil, err
	}

	// if the assignment moved the call to a new server, persist it
	if assignment.ServerID != preferredServerID {
		if newID, perr := uuid.Parse(assignment.ServerID); perr == nil {
			if uerr := s.repo.UpdateCallServer(ctx, call.ID, newID); uerr != nil {
				s.logger.Warn("failed to update dm call server", zap.Error(uerr))
			}
			s.invalidateChannel(ctx, channelUUID)
		}
	}

	participants, perr := s.voiceAssign.GetVoiceParticipants(ctx, channelID)
	if perr != nil {
		return nil, nil, errors.Internal("failed to load participants", perr)
	}
	var result []voiceassign.VoiceParticipant
	for _, p := range participants {
		if p.UserID != userUUID.String() {
			result = append(result, *p)
		}
	}

	if channel, _ := s.repo.GetByID(ctx, channelUUID); channel != nil {
		other := channel.User1ID
		if other == userUUID {
			other = channel.User2ID
		}
		s.hub.BroadcastToUser(other.String(), &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_DmCallStarted{
				DmCallStarted: &streamv1.DMCallStarted{
					ChannelId: channelID, CallerId: userUUID.String(), AudioOnly: audioOnly,
				},
			},
		})
	}

	return assignment, result, nil
}

// broadcastCallStarted notifies only the non-starting participant that a call
// began, via DmCallStarted. It is a no-op if the channel can't be loaded.
func (s *Service) broadcastCallStarted(ctx context.Context, channelID, starterID uuid.UUID, audioOnly bool) {
	channel, _ := s.repo.GetByID(ctx, channelID)
	if channel != nil {
		otherUserID := channel.User1ID
		if otherUserID == starterID {
			otherUserID = channel.User2ID
		}

		// Notify the other user that a call started
		s.hub.BroadcastToUser(otherUserID.String(), &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_DmCallStarted{
				DmCallStarted: &streamv1.DMCallStarted{
					ChannelId: channelID.String(),
					CallerId:  starterID.String(),
					AudioOnly: audioOnly,
				},
			},
		})
	}
}

// LeaveCall removes the caller from a channel's voice session. It sends
// VoiceUserLeft to both the leaver and the peer (the leaver is included
// deliberately, so a DM client waiting on that event can tear down its call UI).
// When no participants remain it ends the active call, invalidates the channel
// cache, and broadcasts DmCallEnded to both users.
func (s *Service) LeaveCall(ctx context.Context, channelID string) error {
	channelUUID, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return err
	}

	if err := s.voiceAssign.LeaveVoice(ctx, channelID, userUUID.String()); err != nil {
		s.logger.Warn("failed to leave voice", zap.Error(err))
	}

	channel, _ := s.repo.GetByID(ctx, channelUUID)

	// The leaver gets VoiceUserLeft too, not just the peer: room calls deliver it
	// to everyone via BroadcastToRoom, so a DM client waiting on the same event to
	// tear down its call UI would otherwise hang after hitting Leave.
	left := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_VoiceUserLeft{
			VoiceUserLeft: &streamv1.VoiceUserLeft{
				RoomId: channelID,
				UserId: userUUID.String(),
			},
		},
	}
	s.hub.BroadcastToUser(userUUID.String(), left)
	if channel != nil {
		otherUserID := channel.User1ID
		if otherUserID == userUUID {
			otherUserID = channel.User2ID
		}
		s.hub.BroadcastToUser(otherUserID.String(), left)
	}

	participants, _ := s.voiceAssign.GetVoiceParticipants(ctx, channelID)
	if len(participants) == 0 {
		_ = s.repo.EndActiveCall(ctx, channelUUID)
		s.invalidateChannel(ctx, channelUUID)

		if channel != nil {
			ended := &streamv1.ServerEvent{
				EventId:   uuid.New().String(),
				CreatedAt: timestamppb.Now(),
				Payload: &streamv1.ServerEvent_DmCallEnded{
					DmCallEnded: &streamv1.DMCallEnded{
						ChannelId: channelID,
						UserId:    userUUID.String(),
					},
				},
			}
			s.hub.BroadcastToUser(channel.User1ID.String(), ended)
			s.hub.BroadcastToUser(channel.User2ID.String(), ended)
		}
	}

	return nil
}

// EndCall forcibly ends a channel's active call regardless of remaining
// participants. Returns NotFound when there is no active call; on success it
// invalidates the channel cache and sends VoiceUserLeft to both users.
func (s *Service) EndCall(ctx context.Context, channelID string) error {
	channelUUID, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return err
	}

	call, err := s.repo.GetActiveCall(ctx, channelUUID)
	if err != nil {
		return errors.Internal("failed to check active call", err)
	}
	if call == nil {
		return errors.NotFound("no active call")
	}

	if err := s.repo.EndCall(ctx, call.ID); err != nil {
		return errors.Internal("failed to end call", err)
	}
	s.invalidateChannel(ctx, channelUUID)
	channel, _ := s.repo.GetByID(ctx, channelUUID)
	if channel != nil {
		s.hub.BroadcastToUser(channel.User1ID.String(), &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_VoiceUserLeft{
				VoiceUserLeft: &streamv1.VoiceUserLeft{
					RoomId: channelID,
					UserId: userUUID.String(),
				},
			},
		})
		s.hub.BroadcastToUser(channel.User2ID.String(), &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_VoiceUserLeft{
				VoiceUserLeft: &streamv1.VoiceUserLeft{
					RoomId: channelID,
					UserId: userUUID.String(),
				},
			},
		})
	}

	return nil
}

// GetCallStatus returns the channel's active call and its voice participants for
// a participant caller, or (nil, nil, nil) when no call is active.
func (s *Service) GetCallStatus(ctx context.Context, channelID string) (*DMCall, []*voiceassign.VoiceParticipant, error) {
	channelUUID, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, nil, err
	}

	call, err := s.repo.GetActiveCall(ctx, channelUUID)
	if err != nil {
		return nil, nil, errors.Internal("failed to get call status", err)
	}

	if call == nil {
		return nil, nil, nil
	}

	participants, _ := s.voiceAssign.GetVoiceParticipants(ctx, channelID)

	return call, participants, nil
}

// broadcastMessageCreated delivers a DmMessageCreated event to both channel
// participants individually. It no-ops if the channel can't be loaded or no hub
// is set.
func (s *Service) broadcastMessageCreated(ctx context.Context, channelID uuid.UUID, msg *DMMessage) {
	channel, _ := s.repo.GetByID(ctx, channelID)
	if channel == nil || s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_DmMessageCreated{
			DmMessageCreated: &streamv1.DMMessageCreated{
				ChannelId: channelID.String(),
				Message:   dmMessageToProto(msg),
			},
		},
	}

	s.hub.BroadcastToUser(channel.User1ID.String(), event)
	s.hub.BroadcastToUser(channel.User2ID.String(), event)
}

// broadcastMessageEdited delivers a MessageEdited event (in the shared common
// message shape) to both channel participants.
func (s *Service) broadcastMessageEdited(ctx context.Context, msg *DMMessage) {
	channel, _ := s.repo.GetByID(ctx, msg.ChannelID)
	if channel == nil || s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageEdited{
			MessageEdited: &streamv1.MessageEdited{
				Message: dmMessageToCommonProto(msg),
			},
		},
	}

	s.hub.BroadcastToUser(channel.User1ID.String(), event)
	s.hub.BroadcastToUser(channel.User2ID.String(), event)
}

// broadcastMessageDeleted delivers a MessageDeleted event to both channel
// participants (the channel id is carried in the event's RoomId field).
func (s *Service) broadcastMessageDeleted(ctx context.Context, channelID uuid.UUID, messageID string) {
	channel, _ := s.repo.GetByID(ctx, channelID)
	if channel == nil || s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageDeleted{
			MessageDeleted: &streamv1.MessageDeleted{
				MessageId: messageID,
				RoomId:    channelID.String(),
			},
		},
	}

	s.hub.BroadcastToUser(channel.User1ID.String(), event)
	s.hub.BroadcastToUser(channel.User2ID.String(), event)
}

// broadcastReactionAdded delivers a MessageReactionAdded event to both channel
// participants.
func (s *Service) broadcastReactionAdded(ctx context.Context, channelID uuid.UUID, messageID string, reaction *DMReaction) {
	channel, _ := s.repo.GetByID(ctx, channelID)
	if channel == nil || s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageReactionAdded{
			MessageReactionAdded: &streamv1.MessageReactionAdded{
				MessageId: messageID,
				RoomId:    channelID.String(),
				Reaction:  dmReactionToProto(reaction),
			},
		},
	}

	s.hub.BroadcastToUser(channel.User1ID.String(), event)
	s.hub.BroadcastToUser(channel.User2ID.String(), event)
}

// broadcastReactionRemoved delivers a MessageReactionRemoved event to both
// channel participants.
func (s *Service) broadcastReactionRemoved(ctx context.Context, channelID uuid.UUID, messageID, reactionID, userID string) {
	channel, _ := s.repo.GetByID(ctx, channelID)
	if channel == nil || s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageReactionRemoved{
			MessageReactionRemoved: &streamv1.MessageReactionRemoved{
				MessageId:  messageID,
				RoomId:     channelID.String(),
				ReactionId: reactionID,
				UserId:     userID,
			},
		},
	}

	s.hub.BroadcastToUser(channel.User1ID.String(), event)
	s.hub.BroadcastToUser(channel.User2ID.String(), event)
}

// broadcastMessagePinned delivers a MessagePinned event to both channel
// participants.
func (s *Service) broadcastMessagePinned(ctx context.Context, channelID uuid.UUID, messageID, pinnedBy string) {
	channel, _ := s.repo.GetByID(ctx, channelID)
	if channel == nil || s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessagePinned{
			MessagePinned: &streamv1.MessagePinned{
				MessageId: messageID,
				RoomId:    channelID.String(),
				PinnedBy:  pinnedBy,
			},
		},
	}

	s.hub.BroadcastToUser(channel.User1ID.String(), event)
	s.hub.BroadcastToUser(channel.User2ID.String(), event)
}

// broadcastMessageUnpinned delivers a MessageUnpinned event to both channel
// participants.
func (s *Service) broadcastMessageUnpinned(ctx context.Context, channelID uuid.UUID, messageID string) {
	channel, _ := s.repo.GetByID(ctx, channelID)
	if channel == nil || s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageUnpinned{
			MessageUnpinned: &streamv1.MessageUnpinned{
				MessageId: messageID,
				RoomId:    channelID.String(),
			},
		},
	}

	s.hub.BroadcastToUser(channel.User1ID.String(), event)
	s.hub.BroadcastToUser(channel.User2ID.String(), event)
}

// GetOtherParticipant returns the id of the channel's other participant relative
// to userID, loading the channel to determine the pair.
func (s *Service) GetOtherParticipant(ctx context.Context, channelID, userID uuid.UUID) (uuid.UUID, error) {
	channel, err := s.repo.GetByID(ctx, channelID)
	if err != nil {
		return uuid.Nil, err
	}

	if channel.User1ID == userID {
		return channel.User2ID, nil
	}
	return channel.User1ID, nil
}

// dmReactionToProto converts a DMReaction to the shared wire reaction shape,
// stringifying the int64 message id.
func dmReactionToProto(reaction *DMReaction) *commonv1.MessageReaction {
	return &commonv1.MessageReaction{
		Id:        reaction.ID.String(),
		MessageId: strconv.FormatInt(reaction.MessageID, 10),
		UserId:    reaction.UserID.String(),
		Emoji:     reaction.Emoji,
		CreatedAt: timestamppb.New(reaction.CreatedAt),
	}
}

// AddReactionAndReturn is AddReaction that also returns the created reaction, for
// RPCs that echo it back to the caller. It broadcasts MessageReactionAdded too.
func (s *Service) AddReactionAndReturn(ctx context.Context, channelID, messageID, emoji string) (*DMReaction, error) {
	_, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, err
	}
	msgID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return nil, errors.BadRequest("invalid message id")
	}
	msg, err := s.msgRepo.GetByID(ctx, msgID)
	if err != nil {
		return nil, err
	}
	reaction, err := s.msgRepo.AddReaction(ctx, msgID, userUUID, emoji)
	if err != nil {
		return nil, err
	}
	s.broadcastReactionAdded(ctx, msg.ChannelID, messageID, reaction)
	return reaction, nil
}

// GetThreadWithParent is GetThread that also returns the parent message. Like
// GetThread, cursor is a numeric offset and nextCursor is empty on the last page.
func (s *Service) GetThreadWithParent(ctx context.Context, channelID, messageID string, limit int, cursor string) (*DMMessage, []*DMMessage, string, error) {
	_, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, nil, "", err
	}
	parentID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return nil, nil, "", errors.BadRequest("invalid message id")
	}
	parent, err := s.msgRepo.GetByID(ctx, parentID)
	if err != nil {
		return nil, nil, "", err
	}
	var offset int64
	if cursor != "" {
		offset, _ = strconv.ParseInt(cursor, 10, 64)
	}
	messages, err := s.msgRepo.GetThreadReplies(ctx, parentID, limit+1, int(offset))
	if err != nil {
		return nil, nil, "", err
	}
	var nextCursor string
	if len(messages) > limit {
		messages = messages[:limit]
		nextCursor = strconv.FormatInt(offset+int64(limit), 10)
	}
	return parent, messages, nextCursor, nil
}

// GetEditHistory returns the recorded prior versions of a DM message for a
// participant, or Internal when no edit reader is configured.
func (s *Service) GetEditHistory(ctx context.Context, channelID, messageID string) ([]editing.Entry, error) {
	_, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, err
	}
	msgID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return nil, errors.BadRequest("invalid message id")
	}
	if s.editReader == nil {
		return nil, errors.Internal("edit history not available", nil)
	}
	return s.editReader.ListDM(ctx, msgID)
}
