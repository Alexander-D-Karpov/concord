package membership

import (
	"context"
	"fmt"
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

// KeyRotator rotates a room's voice encryption key on membership change so a
// removed member can no longer decrypt future media. *voiceassign.Service
// satisfies it.
type KeyRotator interface {
	RotateRoomKey(ctx context.Context, roomID string) error
}

// Service holds the invite and membership business logic. It has no repository of
// its own — it persists through rooms.Repository — so after any mutation it must
// invalidate the rooms membership cache. keyRotator (optional, injected via
// SetKeyRotator) rotates the room's voice key when membership shrinks.
type Service struct {
	roomRepo   *rooms.Repository
	hub        *events.Hub
	cache      *cache.AsidePattern
	keyRotator KeyRotator
}

// SetKeyRotator injects the voice key rotator used when a member is removed;
// until set, removals skip key rotation.
func (s *Service) SetKeyRotator(kr KeyRotator) { s.keyRotator = kr }

// RoomInvite is this package's invite shape (mirrors rooms.RoomInvite). Status is
// one of "pending"/"accepted"/"rejected".
type RoomInvite struct {
	ID            uuid.UUID
	RoomID        uuid.UUID
	InvitedUserID uuid.UUID
	InvitedBy     uuid.UUID
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// RoomInviteWithUsers is an invite denormalized with the room name and both
// users' profiles (this package's mirror of rooms.RoomInviteWithUsers).
type RoomInviteWithUsers struct {
	ID                     uuid.UUID
	RoomID                 uuid.UUID
	RoomName               string
	InvitedUserID          uuid.UUID
	InvitedBy              uuid.UUID
	Status                 string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	InvitedUserHandle      string
	InvitedUserDisplayName string
	InvitedUserAvatarURL   string
	InviterHandle          string
	InviterDisplayName     string
	InviterAvatarURL       string
}

// NewService builds the membership Service. Wire a key rotator afterward via
// SetKeyRotator; a nil aside disables cache invalidation.
func NewService(roomRepo *rooms.Repository, hub *events.Hub, aside *cache.AsidePattern) *Service {
	return &Service{roomRepo: roomRepo, hub: hub, cache: aside}
}

// CreateRoomInvite invites userID to a room on behalf of the caller. The caller
// must be a member (Forbidden otherwise), cannot invite themselves, cannot invite
// an existing member (Conflict), and cannot duplicate a still-pending invite
// (Conflict). On success it broadcasts RoomInviteCreated to the invited user and
// returns the denormalized invite.
func (s *Service) CreateRoomInvite(ctx context.Context, roomID, userID string) (*RoomInviteWithUsers, error) {
	currentUserID := interceptor.GetUserID(ctx)
	if currentUserID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	inviterUUID, err := uuid.Parse(currentUserID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	invitedUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid invited user id")
	}

	if inviterUUID == invitedUUID {
		return nil, errors.BadRequest("cannot invite yourself")
	}

	inviter, err := s.roomRepo.GetMember(ctx, roomUUID, inviterUUID)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, errors.Forbidden("not a member of this room")
		}
		return nil, errors.Internal("failed to check membership", err)
	}

	// Honor the room's who_can_invite policy.
	settings, err := s.roomRepo.GetSettings(ctx, roomUUID)
	if err != nil {
		return nil, errors.Internal("failed to load room settings", err)
	}
	if settings.WhoCanInvite == "moderator" && inviter.Role != "moderator" && inviter.Role != "admin" {
		return nil, errors.Forbidden("only moderators can invite to this room")
	}

	_, err = s.roomRepo.GetMember(ctx, roomUUID, invitedUUID)
	if err == nil {
		return nil, errors.Conflict("user is already a member of this room")
	}
	if !errors.IsNotFound(err) {
		return nil, errors.Internal("failed to check membership", err)
	}

	existing, err := s.roomRepo.GetRoomInviteBetweenUsers(ctx, roomUUID, invitedUUID)
	if err != nil {
		return nil, errors.Internal("failed to check existing invite", err)
	}
	if existing != nil && existing.Status == "pending" {
		return nil, errors.Conflict("invite already sent")
	}

	invite, err := s.roomRepo.CreateRoomInvite(ctx, roomUUID, invitedUUID, inviterUUID)
	if err != nil {
		return nil, errors.Internal("failed to create invite", err)
	}

	inviteWithUsers, err := s.roomRepo.GetRoomInviteWithUsers(ctx, invite.ID)
	if err != nil {
		return nil, errors.Internal("failed to get invite details", err)
	}

	if s.hub != nil {
		protoInvite := toProtoRoomInvite(toRoomInviteWithUsers(inviteWithUsers))
		s.hub.BroadcastToUser(invitedUUID.String(), &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_RoomInviteCreated{
				RoomInviteCreated: &streamv1.RoomInviteCreated{
					Invite: protoInvite,
				},
			},
		})
	}

	return toRoomInviteWithUsers(inviteWithUsers), nil
}

// AcceptRoomInvite accepts a pending invite addressed to the caller: it adds them
// as a "member", marks the invite accepted, and invalidates their room-list and
// membership caches. It then triggers room-join sync and broadcasts
// RoomInviteUpdated (to the invitee) and MemberJoined (to the room). Only the
// invited user may accept (Forbidden), and only pending invites (Conflict).
func (s *Service) AcceptRoomInvite(ctx context.Context, inviteID string) (*rooms.Member, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	inviteUUID, err := uuid.Parse(inviteID)
	if err != nil {
		return nil, errors.BadRequest("invalid invite id")
	}

	invite, err := s.roomRepo.GetRoomInvite(ctx, inviteUUID)
	if err != nil {
		return nil, err
	}

	if invite.InvitedUserID != userUUID {
		return nil, errors.Forbidden("not authorized to accept this invite")
	}

	if invite.Status != "pending" {
		return nil, errors.Conflict("invite already processed")
	}

	// A banned user must not be able to rejoin by accepting an invite.
	if banned, err := s.roomRepo.IsBanned(ctx, invite.RoomID, userUUID); err != nil {
		return nil, errors.Internal("failed to check ban status", err)
	} else if banned {
		return nil, errors.Forbidden("you are banned from this room")
	}

	// Add the member, enforcing the room's member cap atomically (0 = unlimited).
	settings, err := s.roomRepo.GetSettings(ctx, invite.RoomID)
	if err != nil {
		return nil, errors.Internal("failed to load room settings", err)
	}
	if settings.MemberCap > 0 {
		added, err := s.roomRepo.AddMemberIfBelowCap(ctx, invite.RoomID, userUUID, "member", settings.MemberCap)
		if err != nil {
			return nil, errors.Internal("failed to add member", err)
		}
		if !added {
			return nil, errors.Forbidden("room is full")
		}
	} else if err := s.roomRepo.AddMember(ctx, invite.RoomID, userUUID, "member"); err != nil {
		return nil, errors.Internal("failed to add member", err)
	}

	if err := s.roomRepo.UpdateRoomInviteStatus(ctx, inviteUUID, "accepted"); err != nil {
		return nil, errors.Internal("failed to update invite status", err)
	}

	// invalidate cached room list for the user who just joined
	if s.cache != nil {
		_ = s.cache.Invalidate(ctx,
			fmt.Sprintf("u:%s:rooms", userID),
			fmt.Sprintf("m:%s:%s", invite.RoomID, userID),
		)
	}

	updatedInvite, err := s.roomRepo.GetRoomInviteWithUsers(ctx, inviteUUID)
	if err != nil {
		return nil, errors.Internal("failed to get updated invite", err)
	}

	if s.hub != nil {
		s.hub.NotifyRoomJoinSync(userID, invite.RoomID.String())

		protoInvite := toProtoRoomInvite(toRoomInviteWithUsers(updatedInvite))
		s.hub.BroadcastToUser(updatedInvite.InvitedUserID.String(), &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_RoomInviteUpdated{
				RoomInviteUpdated: &streamv1.RoomInviteUpdated{
					Invite: protoInvite,
				},
			},
		})

		s.hub.BroadcastToRoom(invite.RoomID.String(), &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MemberJoined{
				MemberJoined: &streamv1.MemberJoined{
					Member: &commonv1.Member{
						UserId:   userID,
						RoomId:   invite.RoomID.String(),
						Role:     commonv1.Role_ROLE_MEMBER,
						JoinedAt: timestamppb.Now(),
					},
				},
			},
		})
	}

	return s.roomRepo.GetMember(ctx, invite.RoomID, userUUID)
}

// RejectRoomInvite lets the invited user decline a pending invite (setting its
// status to "rejected") and broadcasts RoomInviteUpdated. Only the invitee may
// reject (Forbidden), and only a pending invite (Conflict).
func (s *Service) RejectRoomInvite(ctx context.Context, inviteID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	inviteUUID, err := uuid.Parse(inviteID)
	if err != nil {
		return errors.BadRequest("invalid invite id")
	}

	invite, err := s.roomRepo.GetRoomInvite(ctx, inviteUUID)
	if err != nil {
		return err
	}

	if invite.InvitedUserID != userUUID {
		return errors.Forbidden("not authorized to reject this invite")
	}

	if invite.Status != "pending" {
		return errors.Conflict("invite already processed")
	}

	if err := s.roomRepo.UpdateRoomInviteStatus(ctx, inviteUUID, "rejected"); err != nil {
		return err
	}

	if s.hub != nil {
		updatedInvite, err := s.roomRepo.GetRoomInviteWithUsers(ctx, inviteUUID)
		if err == nil {
			protoInvite := toProtoRoomInvite(toRoomInviteWithUsers(updatedInvite))
			s.hub.BroadcastToUser(updatedInvite.InvitedUserID.String(), &streamv1.ServerEvent{
				EventId:   uuid.New().String(),
				CreatedAt: timestamppb.Now(),
				Payload: &streamv1.ServerEvent_RoomInviteUpdated{
					RoomInviteUpdated: &streamv1.RoomInviteUpdated{
						Invite: protoInvite,
					},
				},
			})
		}
	}

	return nil
}

// CancelRoomInvite lets the inviter withdraw a pending invite they sent (it is
// marked "rejected" in the DB) and broadcasts RoomInviteUpdated. Only the
// original inviter may cancel (Forbidden), and only a pending invite (Conflict).
func (s *Service) CancelRoomInvite(ctx context.Context, inviteID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	inviteUUID, err := uuid.Parse(inviteID)
	if err != nil {
		return errors.BadRequest("invalid invite id")
	}

	invite, err := s.roomRepo.GetRoomInvite(ctx, inviteUUID)
	if err != nil {
		return err
	}

	if invite.InvitedBy != userUUID {
		return errors.Forbidden("not authorized to cancel this invite")
	}

	if invite.Status != "pending" {
		return errors.Conflict("invite already processed")
	}

	if err := s.roomRepo.UpdateRoomInviteStatus(ctx, inviteUUID, "rejected"); err != nil {
		return err
	}

	if s.hub != nil {
		updatedInvite, err := s.roomRepo.GetRoomInviteWithUsers(ctx, inviteUUID)
		if err == nil {
			protoInvite := toProtoRoomInvite(toRoomInviteWithUsers(updatedInvite))
			s.hub.BroadcastToUser(updatedInvite.InvitedUserID.String(), &streamv1.ServerEvent{
				EventId:   uuid.New().String(),
				CreatedAt: timestamppb.Now(),
				Payload: &streamv1.ServerEvent_RoomInviteUpdated{
					RoomInviteUpdated: &streamv1.RoomInviteUpdated{
						Invite: protoInvite,
					},
				},
			})
		}
	}

	return nil
}

// ListRoomInvites returns the caller's pending invites split into incoming
// (addressed to them) and outgoing (sent by them), each with joined profiles.
func (s *Service) ListRoomInvites(ctx context.Context) (incoming, outgoing []*RoomInviteWithUsers, err error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, nil, errors.Unauthorized("user not authenticated")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, nil, errors.BadRequest("invalid user id")
	}

	incomingRaw, err := s.roomRepo.ListIncomingRoomInvitesWithUsers(ctx, userUUID)
	if err != nil {
		return nil, nil, err
	}

	outgoingRaw, err := s.roomRepo.ListOutgoingRoomInvitesWithUsers(ctx, userUUID)
	if err != nil {
		return nil, nil, err
	}

	incoming = make([]*RoomInviteWithUsers, len(incomingRaw))
	for i, inv := range incomingRaw {
		incoming[i] = toRoomInviteWithUsers(inv)
	}

	outgoing = make([]*RoomInviteWithUsers, len(outgoingRaw))
	for i, inv := range outgoingRaw {
		outgoing[i] = toRoomInviteWithUsers(inv)
	}

	return incoming, outgoing, nil
}

// Remove kicks a user from a room. Only an admin caller may do so (Forbidden
// otherwise). After deleting the membership it rotates the room's voice key (so
// the removed member can no longer decrypt future media), invalidates the target
// user's room-list and membership caches, notifies them via room-leave sync, and
// asynchronously broadcasts MemberRemoved to the room.
func (s *Service) Remove(ctx context.Context, roomID, userID string) error {
	callerID := interceptor.GetUserID(ctx)
	if callerID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	callerUUID, err := uuid.Parse(callerID)
	if err != nil {
		return errors.BadRequest("invalid caller id")
	}

	member, err := s.roomRepo.GetMember(ctx, roomUUID, callerUUID)
	if err != nil {
		return err
	}

	if member.Role != "admin" {
		return errors.Forbidden("only admins can remove users")
	}

	targetUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	if err := s.roomRepo.RemoveMember(ctx, roomUUID, targetUUID); err != nil {
		return err
	}

	// Rotate the room's voice key so the removed member can't decrypt future media.
	if s.keyRotator != nil {
		_ = s.keyRotator.RotateRoomKey(ctx, roomID)
	}

	if s.cache != nil {
		_ = s.cache.Invalidate(ctx,
			fmt.Sprintf("u:%s:rooms", userID),
			fmt.Sprintf("m:%s:%s", roomID, userID),
		)
	}

	s.hub.NotifyRoomLeave(userID, roomID)

	go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MemberRemoved{
			MemberRemoved: &streamv1.MemberRemoved{
				RoomId: roomID,
				UserId: userID,
			},
		},
	})

	return nil
}

// SetRole changes a member's role (role string mapped to the proto enum for the
// broadcast). Only an admin caller may do so (Forbidden otherwise). It
// invalidates the target's membership cache and asynchronously broadcasts
// RoleChanged to the room.
func (s *Service) SetRole(ctx context.Context, roomID, userID, role string) error {
	callerID := interceptor.GetUserID(ctx)
	if callerID == "" {
		return errors.Unauthorized("user not authenticated")
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return errors.BadRequest("invalid room id")
	}

	callerUUID, err := uuid.Parse(callerID)
	if err != nil {
		return errors.BadRequest("invalid caller id")
	}

	member, err := s.roomRepo.GetMember(ctx, roomUUID, callerUUID)
	if err != nil {
		return err
	}

	if member.Role != "admin" {
		return errors.Forbidden("only admins can change roles")
	}

	targetUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}

	if err := s.roomRepo.UpdateMemberRole(ctx, roomUUID, targetUUID, role); err != nil {
		return err
	}

	if s.cache != nil {
		_ = s.cache.Invalidate(ctx, fmt.Sprintf("m:%s:%s", roomID, userID))
	}
	protoRole := commonv1.Role_ROLE_MEMBER
	switch role {
	case "admin":
		protoRole = commonv1.Role_ROLE_ADMIN
	case "moderator":
		protoRole = commonv1.Role_ROLE_MODERATOR
	}

	go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_RoleChanged{
			RoleChanged: &streamv1.RoleChanged{
				RoomId:  roomID,
				UserId:  userID,
				NewRole: protoRole,
			},
		},
	})

	return nil
}

// SetNickname sets the caller's own nickname in a room (any member may set their
// own) and asynchronously broadcasts MemberNicknameChanged to the room.
func (s *Service) SetNickname(ctx context.Context, roomID, nickname string) error {
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

	if err := s.roomRepo.UpdateMemberNickname(ctx, roomUUID, userUUID, nickname); err != nil {
		return err
	}

	go s.hub.BroadcastToRoom(roomID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MemberNicknameChanged{
			MemberNicknameChanged: &streamv1.MemberNicknameChanged{
				RoomId:      roomID,
				UserId:      userID,
				NewNickname: nickname,
			},
		},
	})

	return nil
}

// GetMember returns the caller's own membership record in a room.
func (s *Service) GetMember(ctx context.Context, roomID string) (*rooms.Member, error) {
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

	return s.roomRepo.GetMember(ctx, roomUUID, userUUID)
}

// ListMembers returns all members of a room. It does not check the caller's
// membership.
func (s *Service) ListMembers(ctx context.Context, roomID string) ([]*rooms.Member, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	return s.roomRepo.ListMembers(ctx, roomUUID)
}

// toRoomInviteWithUsers maps the rooms package's invite-with-users record into
// this package's mirror type field-for-field.
func toRoomInviteWithUsers(inv *rooms.RoomInviteWithUsers) *RoomInviteWithUsers {
	return &RoomInviteWithUsers{
		ID:                     inv.ID,
		RoomID:                 inv.RoomID,
		RoomName:               inv.RoomName,
		InvitedUserID:          inv.InvitedUserID,
		InvitedBy:              inv.InvitedBy,
		Status:                 inv.Status,
		CreatedAt:              inv.CreatedAt,
		UpdatedAt:              inv.UpdatedAt,
		InvitedUserHandle:      inv.InvitedUserHandle,
		InvitedUserDisplayName: inv.InvitedUserDisplayName,
		InvitedUserAvatarURL:   inv.InvitedUserAvatarURL,
		InviterHandle:          inv.InviterHandle,
		InviterDisplayName:     inv.InviterDisplayName,
		InviterAvatarURL:       inv.InviterAvatarURL,
	}
}
