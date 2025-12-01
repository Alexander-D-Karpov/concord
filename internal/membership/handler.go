package membership

import (
	"context"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	membershipv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/membership/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	membershipv1.UnimplementedMembershipServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

func (h *Handler) Invite(ctx context.Context, req *membershipv1.InviteRequest) (*membershipv1.RoomInvite, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}
	if req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("user_id is required"))
	}

	invite, err := h.service.CreateRoomInvite(ctx, req.RoomId, req.UserId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoRoomInvite(invite), nil
}

func (h *Handler) AcceptRoomInvite(ctx context.Context, req *membershipv1.AcceptRoomInviteRequest) (*commonv1.Member, error) {
	if req.InviteId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("invite_id is required"))
	}

	member, err := h.service.AcceptRoomInvite(ctx, req.InviteId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoMember(member), nil
}

func (h *Handler) RejectRoomInvite(ctx context.Context, req *membershipv1.RejectRoomInviteRequest) (*membershipv1.EmptyResponse, error) {
	if req.InviteId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("invite_id is required"))
	}

	if err := h.service.RejectRoomInvite(ctx, req.InviteId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &membershipv1.EmptyResponse{}, nil
}

func (h *Handler) CancelRoomInvite(ctx context.Context, req *membershipv1.CancelRoomInviteRequest) (*membershipv1.EmptyResponse, error) {
	if req.InviteId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("invite_id is required"))
	}

	if err := h.service.CancelRoomInvite(ctx, req.InviteId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &membershipv1.EmptyResponse{}, nil
}

func (h *Handler) ListRoomInvites(ctx context.Context, req *membershipv1.ListRoomInvitesRequest) (*membershipv1.ListRoomInvitesResponse, error) {
	incoming, outgoing, err := h.service.ListRoomInvites(ctx)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoIncoming := make([]*membershipv1.RoomInvite, len(incoming))
	for i, inv := range incoming {
		protoIncoming[i] = toProtoRoomInvite(inv)
	}

	protoOutgoing := make([]*membershipv1.RoomInvite, len(outgoing))
	for i, inv := range outgoing {
		protoOutgoing[i] = toProtoRoomInvite(inv)
	}

	return &membershipv1.ListRoomInvitesResponse{
		Incoming: protoIncoming,
		Outgoing: protoOutgoing,
	}, nil
}

func (h *Handler) Remove(ctx context.Context, req *membershipv1.RemoveRequest) (*membershipv1.EmptyResponse, error) {
	if req.RoomId == "" || req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and user_id are required"))
	}

	if err := h.service.Remove(ctx, req.RoomId, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &membershipv1.EmptyResponse{}, nil
}

func (h *Handler) SetRole(ctx context.Context, req *membershipv1.SetRoleRequest) (*commonv1.Member, error) {
	if req.RoomId == "" || req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and user_id are required"))
	}

	roleStr := roleToString(req.Role)
	if err := h.service.SetRole(ctx, req.RoomId, req.UserId, roleStr); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &commonv1.Member{
		UserId: req.UserId,
		RoomId: req.RoomId,
		Role:   req.Role,
	}, nil
}

func (h *Handler) SetNickname(ctx context.Context, req *membershipv1.SetNicknameRequest) (*commonv1.Member, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	if err := h.service.SetNickname(ctx, req.RoomId, req.Nickname); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	member, err := h.service.GetMember(ctx, req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoMember(member), nil
}

func (h *Handler) ListMembers(ctx context.Context, req *membershipv1.ListMembersRequest) (*membershipv1.ListMembersResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	members, err := h.service.ListMembers(ctx, req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoMembers := make([]*commonv1.Member, len(members))
	for i, member := range members {
		protoMembers[i] = toProtoMember(member)
	}

	return &membershipv1.ListMembersResponse{
		Members: protoMembers,
	}, nil
}

func toProtoRoomInvite(inv *RoomInviteWithUsers) *membershipv1.RoomInvite {
	status := membershipv1.RoomInviteStatus_ROOM_INVITE_STATUS_PENDING
	switch inv.Status {
	case "accepted":
		status = membershipv1.RoomInviteStatus_ROOM_INVITE_STATUS_ACCEPTED
	case "rejected":
		status = membershipv1.RoomInviteStatus_ROOM_INVITE_STATUS_REJECTED
	}

	return &membershipv1.RoomInvite{
		Id:                     inv.ID.String(),
		RoomId:                 inv.RoomID.String(),
		RoomName:               inv.RoomName,
		InvitedUserId:          inv.InvitedUserID.String(),
		InvitedBy:              inv.InvitedBy.String(),
		Status:                 status,
		CreatedAt:              timestamppb.New(inv.CreatedAt),
		UpdatedAt:              timestamppb.New(inv.UpdatedAt),
		InvitedUserHandle:      inv.InvitedUserHandle,
		InvitedUserDisplayName: inv.InvitedUserDisplayName,
		InvitedUserAvatarUrl:   inv.InvitedUserAvatarURL,
		InviterHandle:          inv.InviterHandle,
		InviterDisplayName:     inv.InviterDisplayName,
		InviterAvatarUrl:       inv.InviterAvatarURL,
	}
}

func toProtoMember(m *rooms.Member) *commonv1.Member {
	if m == nil {
		return nil
	}

	var nickname string
	if m.Nickname != nil {
		nickname = *m.Nickname
	}

	return &commonv1.Member{
		RoomId:   m.RoomID.String(),
		UserId:   m.UserID.String(),
		Role:     stringToRole(m.Role),
		Nickname: nickname,
		Status:   m.Status,
		JoinedAt: timestamppb.New(m.JoinedAt),
	}
}

func roleToString(role commonv1.Role) string {
	switch role {
	case commonv1.Role_ROLE_ADMIN:
		return "admin"
	case commonv1.Role_ROLE_MODERATOR:
		return "moderator"
	default:
		return "member"
	}
}

func stringToRole(role string) commonv1.Role {
	switch role {
	case "admin":
		return commonv1.Role_ROLE_ADMIN
	case "moderator":
		return commonv1.Role_ROLE_MODERATOR
	default:
		return commonv1.Role_ROLE_MEMBER
	}
}
