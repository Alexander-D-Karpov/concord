package readtracking

import (
	"context"
	"strconv"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service coordinates read tracking on top of the Repository and broadcasts read
// receipts and unread-count updates over the hub.
type Service struct {
	repo *Repository
	hub  *events.Hub
}

// NewService wires the read-tracking Service. A nil hub disables broadcasts.
func NewService(repo *Repository, hub *events.Hub) *Service {
	return &Service{
		repo: repo,
		hub:  hub,
	}
}

// MarkRoomAsRead advances the user's last-read position in a room, then returns
// the resulting last-read message ID and unread count. As side effects it
// broadcasts a MessageRead event to the room and an UnreadCountUpdated event to
// the user. The stored position only moves forward (see repo.MarkRoomAsRead).
func (s *Service) MarkRoomAsRead(ctx context.Context, userID, roomID uuid.UUID, messageID int64) (int64, int32, error) {
	if err := s.repo.MarkRoomAsRead(ctx, userID, roomID, messageID); err != nil {
		return 0, 0, err
	}

	status, err := s.repo.GetRoomReadStatus(ctx, userID, roomID)
	if err != nil {
		return 0, 0, err
	}

	unreadCount, err := s.repo.GetRoomUnreadCount(ctx, userID, roomID)
	if err != nil {
		return status.LastReadMessageID, 0, err
	}

	if s.hub != nil {
		s.hub.BroadcastToRoom(roomID.String(), &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageRead{
				MessageRead: &streamv1.MessageRead{
					RoomId:    roomID.String(),
					UserId:    userID.String(),
					MessageId: strconv.FormatInt(messageID, 10),
					ReadAt:    timestamppb.Now(),
				},
			},
		})
	}

	s.BroadcastUnreadUpdate(ctx, userID, &roomID, nil, unreadCount, status.LastReadMessageID)

	return status.LastReadMessageID, unreadCount, nil
}

// MarkDMAsRead advances the user's last-read position in a DM channel and returns
// the resulting last-read ID and unread count. It broadcasts a MessageRead event
// to the other participants only (not the reader). Unlike MarkRoomAsRead it does
// not emit an UnreadCountUpdated event.
func (s *Service) MarkDMAsRead(ctx context.Context, userID, channelID uuid.UUID, messageID int64) (int64, int32, error) {
	if err := s.repo.MarkDMAsRead(ctx, userID, channelID, messageID); err != nil {
		return 0, 0, err
	}

	status, err := s.repo.GetDMReadStatus(ctx, userID, channelID)
	if err != nil {
		return 0, 0, err
	}

	unreadCount, err := s.repo.GetDMUnreadCount(ctx, userID, channelID)
	if err != nil {
		return status.LastReadMessageID, 0, err
	}

	participants, err := s.repo.GetDMChannelParticipants(ctx, channelID)
	if err == nil && s.hub != nil {
		for _, participantID := range participants {
			if participantID != userID {
				s.hub.BroadcastToUser(participantID.String(), &streamv1.ServerEvent{
					EventId:   uuid.New().String(),
					CreatedAt: timestamppb.Now(),
					Payload: &streamv1.ServerEvent_MessageRead{
						MessageRead: &streamv1.MessageRead{
							ChannelId: channelID.String(),
							UserId:    userID.String(),
							MessageId: strconv.FormatInt(messageID, 10),
							ReadAt:    timestamppb.Now(),
						},
					},
				})
			}
		}
	}

	return status.LastReadMessageID, unreadCount, nil
}

// GetRoomLastReadMessageID returns the user's last-read message ID for a room, or
// 0 if they have never read it.
func (s *Service) GetRoomLastReadMessageID(ctx context.Context, userID, roomID uuid.UUID) (int64, error) {
	status, err := s.repo.GetRoomReadStatus(ctx, userID, roomID)
	if err != nil {
		return 0, err
	}
	return status.LastReadMessageID, nil
}

// GetDMLastReadMessageID returns the user's last-read message ID for a DM channel,
// or 0 if they have never read it.
func (s *Service) GetDMLastReadMessageID(ctx context.Context, userID, channelID uuid.UUID) (int64, error) {
	status, err := s.repo.GetDMReadStatus(ctx, userID, channelID)
	if err != nil {
		return 0, err
	}
	return status.LastReadMessageID, nil
}

// GetAllRoomUnreadCounts returns per-room unread info for the user plus the summed
// total unread count across all their rooms.
func (s *Service) GetAllRoomUnreadCounts(ctx context.Context, userID uuid.UUID) ([]RoomUnreadInfo, int32, error) {
	infos, err := s.repo.GetAllRoomUnreadCounts(ctx, userID)
	if err != nil {
		return nil, 0, err
	}

	var total int32
	for _, info := range infos {
		total += info.UnreadCount
	}

	return infos, total, nil
}

// GetAllDMUnreadCounts returns per-channel unread info for the user plus the
// summed total unread count across all their DM channels.
func (s *Service) GetAllDMUnreadCounts(ctx context.Context, userID uuid.UUID) ([]DMUnreadInfo, int32, error) {
	infos, err := s.repo.GetAllDMUnreadCounts(ctx, userID)
	if err != nil {
		return nil, 0, err
	}

	var total int32
	for _, info := range infos {
		total += info.UnreadCount
	}

	return infos, total, nil
}

// BroadcastUnreadUpdate sends an UnreadCountUpdated event only to the given user.
// Pass whichever of roomID/channelID applies (the other nil) to scope the event;
// it is a no-op when the hub is nil.
func (s *Service) BroadcastUnreadUpdate(ctx context.Context, userID uuid.UUID, roomID *uuid.UUID, channelID *uuid.UUID, unreadCount int32, lastMessageID int64) {
	if s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.New(time.Now()),
		Payload: &streamv1.ServerEvent_UnreadCountUpdated{
			UnreadCountUpdated: &streamv1.UnreadCountUpdated{
				UnreadCount:   unreadCount,
				LastMessageId: strconv.FormatInt(lastMessageID, 10),
			},
		},
	}

	if roomID != nil {
		event.GetUnreadCountUpdated().RoomId = roomID.String()
	}
	if channelID != nil {
		event.GetUnreadCountUpdated().ChannelId = channelID.String()
	}

	s.hub.BroadcastToUser(userID.String(), event)
}
