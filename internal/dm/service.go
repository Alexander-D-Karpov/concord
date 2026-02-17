package dm

import (
	"context"
	"strconv"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	repo        *Repository
	msgRepo     *MessageRepository
	usersRepo   *users.Repository
	hub         *events.Hub
	voiceAssign *voiceassign.Service
	logger      *zap.Logger
}

func NewService(repo *Repository, msgRepo *MessageRepository, usersRepo *users.Repository, hub *events.Hub, voiceAssign *voiceassign.Service, logger *zap.Logger) *Service {
	return &Service{
		repo:        repo,
		msgRepo:     msgRepo,
		usersRepo:   usersRepo,
		hub:         hub,
		voiceAssign: voiceAssign,
		logger:      logger,
	}
}

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

func (s *Service) ListDMs(ctx context.Context) ([]*DMChannelWithUser, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	return s.repo.ListByUser(ctx, userUUID)
}

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

func (s *Service) GetChannel(ctx context.Context, channelID string) (*DMChannel, error) {
	channelUUID, err := uuid.Parse(channelID)
	if err != nil {
		return nil, errors.BadRequest("invalid channel id")
	}

	return s.repo.GetByID(ctx, channelUUID)
}

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

	isParticipant, err := s.repo.IsParticipant(ctx, channelUUID, userUUID)
	if err != nil {
		return uuid.Nil, uuid.Nil, errors.Internal("failed to check participation", err)
	}
	if !isParticipant {
		return uuid.Nil, uuid.Nil, errors.Forbidden("not a participant")
	}

	return channelUUID, userUUID, nil
}

func (s *Service) SendMessage(ctx context.Context, channelID, content string, replyToID string, attachments []DMAttachment, mentionUserIDs []string) (*DMMessage, error) {
	channelUUID, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, err
	}

	msg := &DMMessage{
		ChannelID:   channelUUID,
		AuthorID:    userUUID,
		Content:     content,
		Attachments: attachments,
	}

	if replyToID != "" {
		id, err := strconv.ParseInt(replyToID, 10, 64)
		if err != nil {
			return nil, errors.BadRequest("invalid reply_to_id")
		}
		msg.ReplyToID = &id
	}

	if len(mentionUserIDs) > 0 {
		for _, id := range mentionUserIDs {
			if uid, err := uuid.Parse(id); err == nil {
				msg.Mentions = append(msg.Mentions, uid)
			}
		}
	}

	if err := s.msgRepo.Create(ctx, msg); err != nil {
		return nil, errors.Internal("failed to create message", err)
	}

	s.broadcastMessageCreated(ctx, channelUUID, msg)

	return msg, nil
}

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
	if err := s.msgRepo.Update(ctx, msg); err != nil {
		return nil, err
	}

	s.broadcastMessageEdited(ctx, msg)

	return msg, nil
}

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

func (s *Service) ListMessages(ctx context.Context, channelID string, beforeID, afterID *string, limit int) ([]*DMMessage, error) {
	channelUUID, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, err
	}

	var beforeIDInt, afterIDInt *int64
	if beforeID != nil {
		id, err := strconv.ParseInt(*beforeID, 10, 64)
		if err != nil {
			return nil, errors.BadRequest("invalid before_id")
		}
		beforeIDInt = &id
	}
	if afterID != nil {
		id, err := strconv.ParseInt(*afterID, 10, 64)
		if err != nil {
			return nil, errors.BadRequest("invalid after_id")
		}
		afterIDInt = &id
	}

	return s.msgRepo.ListByChannel(ctx, channelUUID, beforeIDInt, afterIDInt, limit)
}

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

func (s *Service) ListPinnedMessages(ctx context.Context, channelID string) ([]*DMMessage, error) {
	channelUUID, _, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, err
	}

	return s.msgRepo.ListPinnedMessages(ctx, channelUUID)
}

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

func (s *Service) StartCall(ctx context.Context, channelID string, audioOnly bool) (*voiceassign.VoiceAssignmentResult, string, error) {
	channelUUID, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return nil, "", err
	}

	existingCall, err := s.repo.GetActiveCall(ctx, channelUUID)
	if err != nil {
		return nil, "", errors.Internal("failed to check active call", err)
	}
	if existingCall != nil {
		return nil, "", errors.Conflict("call already active")
	}

	assignment, err := s.voiceAssign.AssignToVoice(ctx, channelID, userUUID.String(), audioOnly)
	if err != nil {
		return nil, "", errors.Internal("failed to assign voice server", err)
	}

	serverUUID, _ := uuid.Parse(assignment.ServerID)
	call, err := s.repo.CreateCall(ctx, channelUUID, userUUID, &serverUUID)
	if err != nil {
		return nil, "", errors.Internal("failed to create call", err)
	}

	s.broadcastCallStarted(ctx, channelUUID, userUUID, audioOnly)

	return assignment, call.ID.String(), nil
}

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
		s.logger.Info("JoinCall: no active call, starting new one", zap.String("channel_id", channelID))
		assignment, _, err := s.StartCall(ctx, channelID, audioOnly)
		if err != nil {
			return nil, nil, err
		}
		return assignment, []voiceassign.VoiceParticipant{}, nil
	}

	assignment, err := s.voiceAssign.AssignToVoice(ctx, channelID, userUUID.String(), audioOnly)
	if err != nil {
		return nil, nil, errors.Internal("failed to assign voice server", err)
	}

	participants, _ := s.voiceAssign.GetVoiceParticipants(ctx, channelID)
	var result []voiceassign.VoiceParticipant
	for _, p := range participants {
		if p.UserID != userUUID.String() {
			result = append(result, *p)
		}
	}

	channel, _ := s.repo.GetByID(ctx, channelUUID)
	if channel != nil {
		otherUserID := channel.User1ID
		if otherUserID == userUUID {
			otherUserID = channel.User2ID
		}

		s.hub.BroadcastToUser(otherUserID.String(), &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_VoiceUserJoined{
				VoiceUserJoined: &streamv1.VoiceUserJoined{
					RoomId:    channelID,
					UserId:    userUUID.String(),
					AudioOnly: audioOnly,
				},
			},
		})
	}

	return assignment, result, nil
}
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

func (s *Service) LeaveCall(ctx context.Context, channelID string) error {
	channelUUID, userUUID, err := s.checkParticipant(ctx, channelID)
	if err != nil {
		return err
	}

	if err := s.voiceAssign.LeaveVoice(ctx, channelID, userUUID.String()); err != nil {
		s.logger.Warn("failed to leave voice", zap.Error(err))
	}

	channel, _ := s.repo.GetByID(ctx, channelUUID)
	if channel != nil {
		otherUserID := channel.User1ID
		if otherUserID == userUUID {
			otherUserID = channel.User2ID
		}

		s.hub.BroadcastToUser(otherUserID.String(), &streamv1.ServerEvent{
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

	participants, _ := s.voiceAssign.GetVoiceParticipants(ctx, channelID)
	if len(participants) == 0 {
		_ = s.repo.EndActiveCall(ctx, channelUUID)

		if channel != nil {
			s.hub.BroadcastToUser(channel.User1ID.String(), &streamv1.ServerEvent{
				EventId:   uuid.New().String(),
				CreatedAt: timestamppb.Now(),
				Payload: &streamv1.ServerEvent_DmCallEnded{
					DmCallEnded: &streamv1.DMCallEnded{
						ChannelId: channelID,
						UserId:    userUUID.String(),
					},
				},
			})
			s.hub.BroadcastToUser(channel.User2ID.String(), &streamv1.ServerEvent{
				EventId:   uuid.New().String(),
				CreatedAt: timestamppb.Now(),
				Payload: &streamv1.ServerEvent_DmCallEnded{
					DmCallEnded: &streamv1.DMCallEnded{
						ChannelId: channelID,
						UserId:    userUUID.String(),
					},
				},
			})
		}
	}

	return nil
}

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

func dmReactionToProto(reaction *DMReaction) *commonv1.MessageReaction {
	return &commonv1.MessageReaction{
		Id:        reaction.ID.String(),
		MessageId: strconv.FormatInt(reaction.MessageID, 10),
		UserId:    reaction.UserID.String(),
		Emoji:     reaction.Emoji,
		CreatedAt: timestamppb.New(reaction.CreatedAt),
	}
}
