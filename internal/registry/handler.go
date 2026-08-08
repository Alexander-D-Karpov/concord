package registry

import (
	"context"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	registryv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/registry/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler adapts the registry Service to the gRPC RegistryService interface,
// converting between proto and domain types and mapping errors to gRPC status.
type Handler struct {
	registryv1.UnimplementedRegistryServiceServer
	service *Service
}

// NewHandler returns a Handler backed by the given Service.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterServer handles the RegisterServer RPC (machine-authenticated), upserting
// the voice server and storing its shared secret hashed. A missing or unparseable
// server ID is replaced with a freshly generated UUID. Requires a server payload.
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

	if req.JwksUrl != "" {
		server.JWKSUrl = &req.JwksUrl
	}

	registered, err := h.service.RegisterServer(ctx, server, req.SharedSecret)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &registryv1.RegisterServerResponse{
		Server: toProtoServer(registered),
	}, nil
}

// Heartbeat handles the Heartbeat RPC, updating the server's load metrics. It
// trusts only the server ID established by machine auth (AuthenticatedServerID),
// returning Unauthorized if unauthenticated and Forbidden if the request targets a
// different server ID than the authenticated one.
func (h *Handler) Heartbeat(ctx context.Context, req *registryv1.HeartbeatRequest) (*registryv1.EmptyResponse, error) {
	authedID := AuthenticatedServerID(ctx)
	if authedID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("server not authenticated"))
	}

	serverID, err := uuid.Parse(authedID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("invalid authenticated server id"))
	}

	if req.ServerId != "" && req.ServerId != authedID {
		return nil, errors.ToGRPCError(errors.Forbidden("cannot heartbeat for another server"))
	}

	if err := h.service.Heartbeat(ctx, serverID, req.ActiveRooms, req.ActiveSessions, req.Cpu, req.OutboundMbps); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &registryv1.EmptyResponse{}, nil
}

// ListServers handles the ListServers RPC, returning online voice servers ordered
// by ascending load. An empty region filter returns servers from all regions.
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
		protoServers[i] = toProtoServer(server)
	}

	return &registryv1.ListServersResponse{Servers: protoServers}, nil
}

// toProtoServer converts a domain VoiceServer to its proto representation. It
// deliberately omits the secret hash so secrets are never sent to clients.
func toProtoServer(s *VoiceServer) *commonv1.VoiceServer {
	return &commonv1.VoiceServer{
		Id:           s.ID.String(),
		Name:         s.Name,
		Region:       s.Region,
		AddrUdp:      s.AddrUDP,
		AddrCtrl:     s.AddrCtrl,
		Status:       s.Status,
		CapacityHint: s.CapacityHint,
		LoadScore:    s.LoadScore,
		UpdatedAt:    timestamppb.New(s.UpdatedAt),
	}
}
