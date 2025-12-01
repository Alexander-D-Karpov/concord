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

type Service struct {
	roomRepo *rooms.Repository
	hub      *events.Hub
	cache    *cache.AsidePattern
}

type RoomInvite struct {
	ID            uuid.UUID
	RoomID        uuid.UUID
	InvitedUserID uuid.UUID
	InvitedBy     uuid.UUID
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

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

func NewService(roomRepo *rooms.Repository, hub *events.Hub, aside *cache.AsidePattern) *Service {
	return &Service{roomRepo: roomRepo, hub: hub, cache: aside}
}

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

	_, err = s.roomRepo.GetMember(ctx, roomUUID, inviterUUID)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, errors.Forbidden("not a member of this room")
		}
		return nil, errors.Internal("failed to check membership", err)
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
		s.hub.BroadcastToUser(userID, &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_RoomInviteReceived{
				RoomInviteReceived: &streamv1.RoomInviteReceived{
					RoomId:             roomID,
					RoomName:           inviteWithUsers.RoomName,
					InvitedBy:          currentUserID,
					InviterDisplayName: inviteWithUsers.InviterDisplayName,
				},
			},
		})
	}

	return toRoomInviteWithUsers(inviteWithUsers), nil
}

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

	if err := s.roomRepo.AddMember(ctx, invite.RoomID, userUUID, "member"); err != nil {
		return nil, errors.Internal("failed to add member", err)
	}

	if err := s.roomRepo.UpdateRoomInviteStatus(ctx, inviteUUID, "accepted"); err != nil {
		return nil, errors.Internal("failed to update invite status", err)
	}

	if s.hub != nil {
		s.hub.NotifyRoomJoinSync(userID, invite.RoomID.String())
	}

	if s.hub != nil {
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

	return s.roomRepo.UpdateRoomInviteStatus(ctx, inviteUUID, "rejected")
}

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

	return s.roomRepo.UpdateRoomInviteStatus(ctx, inviteUUID, "rejected")
}

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

func (s *Service) ListMembers(ctx context.Context, roomID string) ([]*rooms.Member, error) {
	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		return nil, errors.BadRequest("invalid room id")
	}

	return s.roomRepo.ListMembers(ctx, roomUUID)
}

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
