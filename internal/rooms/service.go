package rooms

import (
	"context"
	"fmt"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service holds room CRUD business logic: it enforces admin-only mutations,
// caches per-user room lists, and broadcasts room lifecycle events via the hub.
// cache may be nil, disabling the user-rooms list cache.
type Service struct {
	repo  *Repository
	hub   *events.Hub
	cache *cache.AsidePattern
}

// NewService assembles the rooms Service; a nil aside disables list caching.
func NewService(repo *Repository, hub *events.Hub, aside *cache.AsidePattern) *Service {
	return &Service{
		repo:  repo,
		hub:   hub,
		cache: aside,
	}
}

// CreateRoom creates a room owned by the caller and adds the caller as its first
// member with the "admin" role. An unparseable voiceServerID is ignored (left
// unset) rather than rejected. On success it triggers the caller's room-join sync
// and invalidates their cached room list.
func (s *Service) CreateRoom(ctx context.Context, name string, voiceServerID *string, region *string, description string, isPrivate bool) (*Room, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	room := &Room{
		ID:          uuid.New(),
		Name:        name,
		CreatedBy:   userUUID,
		IsPrivate:   isPrivate,
		Description: description,
	}

	if region != nil {
		room.Region = *region
	}

	if voiceServerID != nil {
		vsID, err := uuid.Parse(*voiceServerID)
		if err == nil {
			room.VoiceServerID = &vsID
		}
	}

	if err := s.repo.Create(ctx, room); err != nil {
		return nil, errors.Internal("failed to create room", err)
	}

	if err := s.repo.AddMember(ctx, room.ID, userUUID, "admin"); err != nil {
		return nil, errors.Internal("failed to add creator as member", err)
	}

	if s.hub != nil {
		s.hub.NotifyRoomJoinSync(userID, room.ID.String())
	}

	if s.cache != nil {
		_ = s.cache.Invalidate(ctx, fmt.Sprintf("u:%s:rooms", userID))
	}

	return room, nil
}

// GetRoom returns a room by id string. It performs no membership check, so
// callers needing access control must gate it separately.
func (s *Service) GetRoom(ctx context.Context, id string) (*Room, error) {
	roomID, err := uuid.Parse(id)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	return s.repo.GetByID(ctx, roomID)
}

// UpdateRoom applies partial updates (nil pointers leave a field unchanged; an
// empty name is ignored) to a room. Only an admin member may update it; on
// success it broadcasts RoomUpdated to the room.
func (s *Service) UpdateRoom(ctx context.Context, roomID string, name *string, description *string, isPrivate *bool) (*Room, error) {
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

	member, err := s.repo.GetMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, errors.Forbidden("not a member of this room")
	}

	if member.Role != "admin" {
		return nil, errors.Forbidden("only admins can update room settings")
	}

	room, err := s.repo.GetByID(ctx, roomUUID)
	if err != nil {
		return nil, err
	}

	if name != nil && *name != "" {
		room.Name = *name
	}
	if description != nil {
		room.Description = *description
	}
	if isPrivate != nil {
		room.IsPrivate = *isPrivate
	}

	if err := s.repo.Update(ctx, room); err != nil {
		return nil, errors.Internal("failed to update room", err)
	}

	if s.hub != nil {
		s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_RoomUpdated{
				RoomUpdated: &streamv1.RoomUpdated{
					Room: toProtoRoom(room),
				},
			},
		})
	}
	return room, nil
}

// DeleteRoom soft-deletes a room. Only an admin member may delete it; on success
// it asynchronously broadcasts RoomDeleted (tagged with the deleting user) to the
// room.
func (s *Service) DeleteRoom(ctx context.Context, roomID string) error {
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

	member, err := s.repo.GetMember(ctx, roomUUID, userUUID)
	if err != nil {
		return errors.Forbidden("not a member of this room")
	}

	if member.Role != "admin" {
		return errors.Forbidden("only admins can delete rooms")
	}

	if err := s.repo.Delete(ctx, roomUUID); err != nil {
		return errors.Internal("failed to delete room", err)
	}

	go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_RoomDeleted{
			RoomDeleted: &streamv1.RoomDeleted{
				RoomId:    roomID,
				DeletedBy: userID,
			},
		},
	})

	return nil
}

// ListRoomsForUser returns the caller's rooms, read-through cached under
// "u:<user>:rooms" for 30s and falling back to a direct repo query on cache
// miss or error.
func (s *Service) ListRoomsForUser(ctx context.Context) ([]*Room, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	const ttl = 30 * time.Second
	key := fmt.Sprintf("u:%s:rooms", userID)

	if s.cache != nil {
		v, err := s.cache.GetOrLoad(ctx, key, ttl, func() (interface{}, error) {
			return s.repo.ListByUser(ctx, userUUID)
		})
		if err == nil {
			if rooms, ok := v.([]*Room); ok {
				return rooms, nil
			}
		}
	}

	return s.repo.ListByUser(ctx, userUUID)
}

// AttachVoiceServer assigns a voice server to a room and returns the refreshed
// room. Only an admin member may do this.
func (s *Service) AttachVoiceServer(ctx context.Context, roomID string, voiceServerID string) (*Room, error) {
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

	serverUUID, err := uuid.Parse(voiceServerID)
	if err != nil {
		return nil, errors.BadRequest("invalid voice server id")
	}

	member, err := s.repo.GetMember(ctx, roomUUID, userUUID)
	if err != nil {
		return nil, errors.Forbidden("not a member of this room")
	}

	if member.Role != "admin" {
		return nil, errors.Forbidden("only admins can assign voice servers")
	}

	if err := s.repo.AssignVoiceServer(ctx, roomUUID, serverUUID); err != nil {
		return nil, errors.Internal("failed to assign voice server", err)
	}

	return s.repo.GetByID(ctx, roomUUID)
}
