package typing

import (
	"context"
	"sync"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service coordinates typing indicators: it persists them, rate-limits per
// user/target in memory, and broadcasts start/stop events over the events hub.
// lastTyped is guarded by rateMu and is per-process (not shared across replicas).
type Service struct {
	repo      *Repository
	hub       *events.Hub
	usersRepo *users.Repository
	rateMu    sync.Mutex
	lastTyped map[string]time.Time
}

// NewService constructs a Service. hub may be nil, in which case broadcasts are
// silently skipped.
func NewService(repo *Repository, hub *events.Hub, usersRepo *users.Repository) *Service {
	return &Service{
		repo:      repo,
		hub:       hub,
		usersRepo: usersRepo,
		lastTyped: make(map[string]time.Time),
	}
}

// typingRateLimit is the minimum spacing between accepted typing events for a
// given user/target pair.
const typingRateLimit = 2 * time.Second

// checkTypingRate reports whether a typing event from userID toward targetID is
// allowed now, returning false if one was accepted within typingRateLimit. On
// acceptance it records the timestamp (side effect) and, once the map exceeds
// 10,000 entries, opportunistically evicts entries older than 2×typingRateLimit
// to bound memory.
func (s *Service) checkTypingRate(userID uuid.UUID, targetID uuid.UUID) bool {
	key := userID.String() + ":" + targetID.String()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()

	now := time.Now()
	if last, ok := s.lastTyped[key]; ok && now.Sub(last) < typingRateLimit {
		return false
	}
	s.lastTyped[key] = now

	if len(s.lastTyped) > 10000 {
		cutoff := now.Add(-typingRateLimit * 2)
		for k, t := range s.lastTyped {
			if t.Before(cutoff) {
				delete(s.lastTyped, k)
			}
		}
	}

	return true
}

// StartTypingInRoom records and broadcasts that the user is typing in a room.
// Rate-limited calls are silently dropped (returns nil without persisting or
// broadcasting).
func (s *Service) StartTypingInRoom(ctx context.Context, userID, roomID uuid.UUID) error {
	if !s.checkTypingRate(userID, roomID) {
		return nil
	}

	if err := s.repo.SetTypingInRoom(ctx, userID, roomID); err != nil {
		return err
	}

	s.broadcastTypingStarted(ctx, userID, &roomID, nil)
	return nil
}

// StopTypingInRoom clears the user's room typing indicator and broadcasts a stop
// event. Unlike the start path it is not rate-limited.
func (s *Service) StopTypingInRoom(ctx context.Context, userID, roomID uuid.UUID) error {
	if err := s.repo.ClearTypingInRoom(ctx, userID, roomID); err != nil {
		return err
	}

	s.broadcastTypingStopped(userID, &roomID, nil)
	return nil
}

// StartTypingInDM records and broadcasts DM typing, delivering the event only to
// otherUserID (the recipient). Rate-limited calls are silently dropped.
func (s *Service) StartTypingInDM(ctx context.Context, userID, channelID uuid.UUID, otherUserID uuid.UUID) error {
	if !s.checkTypingRate(userID, channelID) {
		return nil
	}

	if err := s.repo.SetTypingInDM(ctx, userID, channelID); err != nil {
		return err
	}

	s.broadcastTypingStartedToUser(ctx, userID, channelID, otherUserID)
	return nil
}

// StopTypingInDM clears the user's DM typing indicator and broadcasts a stop event
// to otherUserID. Not rate-limited.
func (s *Service) StopTypingInDM(ctx context.Context, userID, channelID uuid.UUID, otherUserID uuid.UUID) error {
	if err := s.repo.ClearTypingInDM(ctx, userID, channelID); err != nil {
		return err
	}

	s.broadcastTypingStoppedToUser(userID, channelID, otherUserID)
	return nil
}

// broadcastTypingStarted emits a TypingStarted event to a room, enriched with the
// user's display name (best-effort; blank if the lookup fails) and an expiry of
// now + TypingDuration. It is a no-op when hub is nil or roomID is nil; the
// channelID parameter is currently unused.
func (s *Service) broadcastTypingStarted(ctx context.Context, userID uuid.UUID, roomID *uuid.UUID, channelID *uuid.UUID) {
	if s.hub == nil {
		return
	}

	var displayName string
	user, err := s.usersRepo.GetByID(ctx, userID)
	if err == nil {
		displayName = user.DisplayName
	}

	expiresAt := time.Now().Add(TypingDuration)

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_TypingStarted{
			TypingStarted: &streamv1.TypingStarted{
				UserId:          userID.String(),
				UserDisplayName: displayName,
				ExpiresAt:       timestamppb.New(expiresAt),
			},
		},
	}

	if roomID != nil {
		event.GetTypingStarted().RoomId = roomID.String()
		s.hub.BroadcastToRoom(roomID.String(), event)
	}
}

// broadcastTypingStopped emits a TypingStopped event to a room. No-op when hub is
// nil or roomID is nil; the channelID parameter is currently unused.
func (s *Service) broadcastTypingStopped(userID uuid.UUID, roomID *uuid.UUID, channelID *uuid.UUID) {
	if s.hub == nil {
		return
	}

	event := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_TypingStopped{
			TypingStopped: &streamv1.TypingStopped{
				UserId: userID.String(),
			},
		},
	}

	if roomID != nil {
		event.GetTypingStopped().RoomId = roomID.String()
		s.hub.BroadcastToRoom(roomID.String(), event)
	}
}

// broadcastTypingStartedToUser sends a DM TypingStarted event directly to
// recipientID, carrying the typer's display name (best-effort) and expiry. No-op
// when hub is nil.
func (s *Service) broadcastTypingStartedToUser(ctx context.Context, typerID, channelID, recipientID uuid.UUID) {
	if s.hub == nil {
		return
	}

	var displayName string
	user, err := s.usersRepo.GetByID(ctx, typerID)
	if err == nil {
		displayName = user.DisplayName
	}

	expiresAt := time.Now().Add(TypingDuration)

	s.hub.BroadcastToUser(recipientID.String(), &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_TypingStarted{
			TypingStarted: &streamv1.TypingStarted{
				ChannelId:       channelID.String(),
				UserId:          typerID.String(),
				UserDisplayName: displayName,
				ExpiresAt:       timestamppb.New(expiresAt),
			},
		},
	})
}

// broadcastTypingStoppedToUser sends a DM TypingStopped event directly to
// recipientID. No-op when hub is nil.
func (s *Service) broadcastTypingStoppedToUser(typerID, channelID, recipientID uuid.UUID) {
	if s.hub == nil {
		return
	}

	s.hub.BroadcastToUser(recipientID.String(), &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_TypingStopped{
			TypingStopped: &streamv1.TypingStopped{
				ChannelId: channelID.String(),
				UserId:    typerID.String(),
			},
		},
	})
}

// CleanupExpired reaps expired typing indicators and broadcasts a stop event for
// each, so clients clear stale indicators; it returns the number reaped. For DM
// rows it looks up both participants and notifies everyone except the typer.
// concord-api runs this on a short interval.
func (s *Service) CleanupExpired(ctx context.Context) (int64, error) {
	expired, err := s.repo.GetAndDeleteExpired(ctx)
	if err != nil {
		return 0, err
	}

	for _, ind := range expired {
		if ind.RoomID != nil {
			s.broadcastTypingStopped(ind.UserID, ind.RoomID, nil)
		} else if ind.ChannelID != nil {
			participants, err := s.getDMParticipants(ctx, *ind.ChannelID)
			if err == nil {
				for _, p := range participants {
					if p != ind.UserID {
						s.broadcastTypingStoppedToUser(ind.UserID, *ind.ChannelID, p)
					}
				}
			}
		}
	}

	return int64(len(expired)), nil
}

// getDMParticipants returns the two user IDs of a DM channel, used to fan out DM
// typing-stop events to the non-typing side.
func (s *Service) getDMParticipants(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error) {
	query := `SELECT user1_id, user2_id FROM dm_channels WHERE id = $1`

	var user1, user2 uuid.UUID
	err := s.repo.pool.QueryRow(ctx, query, channelID).Scan(&user1, &user2)
	if err != nil {
		return nil, err
	}

	return []uuid.UUID{user1, user2}, nil
}
