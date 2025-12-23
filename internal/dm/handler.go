package dm

import (
	"context"

	dmv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/dm/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	dmv1.UnimplementedDMServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) GetOrCreateDM(ctx context.Context, req *dmv1.GetOrCreateDMRequest) (*dmv1.GetOrCreateDMResponse, error) {
	if req.UserId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("user_id is required"))
	}

	channel, err := h.service.GetOrCreateDM(ctx, req.UserId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.GetOrCreateDMResponse{
		Channel: &dmv1.DMChannel{
			Id:        channel.ID.String(),
			User1Id:   channel.User1ID.String(),
			User2Id:   channel.User2ID.String(),
			CreatedAt: timestamppb.New(channel.CreatedAt),
			UpdatedAt: timestamppb.New(channel.UpdatedAt),
		},
	}, nil
}

func (h *Handler) ListDMs(ctx context.Context, req *dmv1.ListDMsRequest) (*dmv1.ListDMsResponse, error) {
	channels, err := h.service.ListDMs(ctx)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoChannels := make([]*dmv1.DMChannelWithUser, len(channels))
	for i, ch := range channels {
		protoChannels[i] = &dmv1.DMChannelWithUser{
			Channel: &dmv1.DMChannel{
				Id:        ch.Channel.ID.String(),
				User1Id:   ch.Channel.User1ID.String(),
				User2Id:   ch.Channel.User2ID.String(),
				CreatedAt: timestamppb.New(ch.Channel.CreatedAt),
				UpdatedAt: timestamppb.New(ch.Channel.UpdatedAt),
			},
			OtherUserId:      ch.OtherUserID.String(),
			OtherUserHandle:  ch.OtherUserHandle,
			OtherUserDisplay: ch.OtherUserDisplay,
			OtherUserAvatar:  ch.OtherUserAvatar,
			OtherUserStatus:  ch.OtherUserStatus,
		}
	}

	return &dmv1.ListDMsResponse{
		Channels: protoChannels,
	}, nil
}

func (h *Handler) CloseDM(ctx context.Context, req *dmv1.CloseDMRequest) (*dmv1.EmptyResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("user not authenticated"))
	}

	if req.ChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("channel_id is required"))
	}

	if err := h.service.CloseDM(ctx, req.ChannelId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &dmv1.EmptyResponse{}, nil
}
