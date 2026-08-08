package features

import (
	"context"

	featuresv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/features/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/polls"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/slowmode"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Aggregator presents one FeaturesService gRPC surface by embedding the legacy
// monolithic Service and overriding the poll and slow-mode RPCs to delegate to the
// newer per-feature services. Embedding means every non-overridden Service method
// is exposed unchanged.
type Aggregator struct {
	*Service
	polls    *polls.Service
	slowmode *slowmode.Service
}

// NewAggregator composes the base Service with the polls and slow-mode services.
func NewAggregator(base *Service, pollsSvc *polls.Service, slowmodeSvc *slowmode.Service) *Aggregator {
	return &Aggregator{
		Service:  base,
		polls:    pollsSvc,
		slowmode: slowmodeSvc,
	}
}

// CreatePoll overrides the base Service implementation, delegating to the polls
// service.
func (a *Aggregator) CreatePoll(ctx context.Context, req *featuresv1.CreatePollRequest) (*featuresv1.CreatePollResponse, error) {
	return a.polls.Create(ctx, req)
}

// VotePoll overrides the base Service implementation, delegating to the polls
// service.
func (a *Aggregator) VotePoll(ctx context.Context, req *featuresv1.VotePollRequest) (*featuresv1.VotePollResponse, error) {
	return a.polls.Vote(ctx, req)
}

// ClosePoll overrides the base Service implementation, delegating to the polls
// service.
func (a *Aggregator) ClosePoll(ctx context.Context, req *featuresv1.ClosePollRequest) (*emptypb.Empty, error) {
	return a.polls.ClosePoll(ctx, req)
}

// SetSlowMode overrides the base Service implementation, delegating to the
// slow-mode service after validating the room ID. Returns BadRequest for an
// invalid room ID and Internal if the update fails.
func (a *Aggregator) SetSlowMode(ctx context.Context, req *featuresv1.SetSlowModeRequest) (*emptypb.Empty, error) {
	roomID, err := uuid.Parse(req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid room id"))
	}
	if err := a.slowmode.Set(ctx, roomID, req.GetIntervalSeconds()); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("set slow mode failed", err))
	}
	return &emptypb.Empty{}, nil
}
