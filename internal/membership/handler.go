package membership

import (
	"context"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	membershipv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/membership/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler is the MembershipService gRPC server, a thin translation layer over
// the membership Service.
type Handler struct {
	membershipv1.UnimplementedMembershipServiceServer
	service *Service
}

// NewHandler constructs the MembershipService handler.
func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

// Invite handles the Invite RPC, creating a room invite for req.UserId; the
// service enforces that the caller is a member and dedupes existing members and
// pending invites.
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

// AcceptRoomInvite handles the AcceptRoomInvite RPC, returning the resulting
// membership; invitee-only enforcement lives in the service.
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

// RejectRoomInvite handles the RejectRoomInvite RPC (invitee declines).
func (h *Handler) RejectRoomInvite(ctx context.Context, req *membershipv1.RejectRoomInviteRequest) (*membershipv1.EmptyResponse, error) {
	if req.InviteId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("invite_id is required"))
	}

	if err := h.service.RejectRoomInvite(ctx, req.InviteId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &membershipv1.EmptyResponse{}, nil
}

// CancelRoomInvite handles the CancelRoomInvite RPC (inviter withdraws).
func (h *Handler) CancelRoomInvite(ctx context.Context, req *membershipv1.CancelRoomInviteRequest) (*membershipv1.EmptyResponse, error) {
	if req.InviteId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("invite_id is required"))
	}

	if err := h.service.CancelRoomInvite(ctx, req.InviteId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &membershipv1.EmptyResponse{}, nil
}

// ListRoomInvites handles the ListRoomInvites RPC, returning the caller's pending
// incoming and outgoing invites.
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

// Remove handles the Remove RPC (kick a member); admin-only enforcement and key
// rotation live in the service.
func (h *Handler) Remove(ctx context.Context, req *membershipv1.RemoveRequest) (*membershipv1.EmptyResponse, error) {
	if req.RoomId == "" || req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and user_id are required"))
	}

	if err := h.service.Remove(ctx, req.RoomId, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &membershipv1.EmptyResponse{}, nil
}

// SetRole handles the SetRole RPC. It maps the proto role to its string form for
// the service and echoes back a minimal Member with the requested role (not a
// re-read of the stored record); admin-only enforcement lives in the service.
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

// SetNickname handles the SetNickname RPC, setting the caller's own nickname and
// returning their refreshed membership record.
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

// ListMembers handles the ListMembers RPC, returning all members of a room.
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

// toProtoRoomInvite converts a denormalized invite to its wire form, mapping the
// status string to the proto enum (defaulting to PENDING for unknown values).
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

// toProtoMember converts a rooms.Member to the wire commonv1.Member (nil-safe),
// flattening the optional nickname pointer to a plain string and mapping the role
// string to the proto enum.
func toProtoMember(m *rooms.Member) *commonv1.Member {
	if m == nil {
		return nil
	}

	var nickname string
	if m.Nickname != nil {
		nickname = *m.Nickname
	}

	return &commonv1.Member{
		RoomId:            m.RoomID.String(),
		UserId:            m.UserID.String(),
		Role:              stringToRole(m.Role),
		Nickname:          nickname,
		Status:            m.Status,
		JoinedAt:          timestamppb.New(m.JoinedAt),
		LastReadMessageId: m.LastReadMessageID,
	}
}

// roleToString maps the proto role enum to its stored string form, defaulting to
// "member" for unspecified/unknown roles.
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

// stringToRole maps a stored role string to the proto role enum, defaulting to
// ROLE_MEMBER for unknown values.
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
