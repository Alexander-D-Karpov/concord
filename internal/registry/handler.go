package registry

import (
	"context"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	registryv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/registry/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	registryv1.UnimplementedRegistryServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

func (h *Handler) RegisterServer(ctx context.Context, req *registryv1.RegisterServerRequest) (*registryv1.RegisterServerResponse, error) {
	if req.Server == nil {
		return nil, errors.ToGRPCError(errors.BadRequest("server is required"))
	}

	serverID, err := uuid.Parse(req.Server.Id)
	if err != nil {
		serverID = uuid.New()
	}

	server := &VoiceServer{
		ID:           serverID,
		Name:         req.Server.Name,
		Region:       req.Server.Region,
		AddrUDP:      req.Server.AddrUdp,
		AddrCtrl:     req.Server.AddrCtrl,
		Status:       req.Server.Status,
		CapacityHint: req.Server.CapacityHint,
		LoadScore:    req.Server.LoadScore,
	}

	if req.SharedSecret != "" {
		server.SharedSecret = &req.SharedSecret
	}
	if req.JwksUrl != "" {
		server.JWKSUrl = &req.JwksUrl
	}

	registered, err := h.service.RegisterServer(ctx, server)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &registryv1.RegisterServerResponse{
		Server: &commonv1.VoiceServer{
			Id:           registered.ID.String(),
			Name:         registered.Name,
			Region:       registered.Region,
			AddrUdp:      registered.AddrUDP,
			AddrCtrl:     registered.AddrCtrl,
			Status:       registered.Status,
			CapacityHint: registered.CapacityHint,
			LoadScore:    registered.LoadScore,
			UpdatedAt:    timestamppb.New(registered.UpdatedAt),
		},
	}, nil
}

func (h *Handler) Heartbeat(ctx context.Context, req *registryv1.HeartbeatRequest) (*registryv1.EmptyResponse, error) {
	if req.ServerId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("server_id is required"))
	}

	serverID, err := uuid.Parse(req.ServerId)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid server_id"))
	}

	if err := h.service.Heartbeat(ctx, serverID, req.ActiveRooms, req.ActiveSessions, req.Cpu, req.OutboundMbps); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &registryv1.EmptyResponse{}, nil
}

func (h *Handler) ListServers(ctx context.Context, req *registryv1.ListServersRequest) (*registryv1.ListServersResponse, error) {
	var region *string
	if req.Region != "" {
		region = &req.Region
	}

	servers, err := h.service.ListServers(ctx, region)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoServers := make([]*commonv1.VoiceServer, len(servers))
	for i, server := range servers {
		protoServers[i] = &commonv1.VoiceServer{
			Id:           server.ID.String(),
			Name:         server.Name,
			Region:       server.Region,
			AddrUdp:      server.AddrUDP,
			AddrCtrl:     server.AddrCtrl,
			Status:       server.Status,
			CapacityHint: server.CapacityHint,
			LoadScore:    server.LoadScore,
			UpdatedAt:    timestamppb.New(server.UpdatedAt),
		}
	}

	return &registryv1.ListServersResponse{
		Servers: protoServers,
	}, nil
}
