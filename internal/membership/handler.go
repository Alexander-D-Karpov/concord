package membership

import (
	"context"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	membershipv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/membership/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
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

func (h *Handler) Invite(ctx context.Context, req *membershipv1.InviteRequest) (*commonv1.Member, error) {
	if req.RoomId == "" || req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id and user_id are required"))
	}

	if err := h.service.Invite(ctx, req.RoomId, req.UserId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &commonv1.Member{
		UserId:   req.UserId,
		RoomId:   req.RoomId,
		Role:     commonv1.Role_ROLE_MEMBER,
		JoinedAt: timestamppb.Now(),
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
		protoMembers[i] = &commonv1.Member{
			UserId:   member.UserID.String(),
			RoomId:   member.RoomID.String(),
			Role:     stringToRole(member.Role),
			JoinedAt: timestamppb.New(member.JoinedAt),
		}
	}

	return &membershipv1.ListMembersResponse{
		Members: protoMembers,
	}, nil
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
