package control

import (
	"context"
	"fmt"
	"net"

	registryv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/registry/v1"
	"github.com/Alexander-D-Karpov/concord/internal/version"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// Server is the voice node's gRPC control-plane endpoint. It implements the
// RegistryService server interface but currently only answers Heartbeat probes
// (the voice node is a registry client, not a registry); the embedded
// Unimplemented base stubs the rest and keeps forward compatibility.
type Server struct {
	registryv1.UnimplementedRegistryServiceServer
	sessionManager *session.Manager
	logger         *zap.Logger
	serverID       string
	region         string
	name           string
	capacity       int32
}

// NewServer builds the control server. name is suffixed with the voice build
// version (as "name/vX") so peers can see which binary is running.
func NewServer(
	sessionManager *session.Manager,
	logger *zap.Logger,
	serverID, region, name string,
	capacity int32,
) *Server {
	return &Server{
		sessionManager: sessionManager,
		logger:         logger,
		serverID:       serverID,
		region:         region,
		name:           fmt.Sprintf("%s/v%s", name, version.Voice()),
		capacity:       capacity,
	}
}

// Start listens on the given TCP port and serves gRPC until ctx is cancelled
// (then it GracefulStops and returns nil) or Serve fails. Blocks for the
// server's lifetime; returns the listen/serve error otherwise.
func (s *Server) Start(ctx context.Context, port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	grpcServer := grpc.NewServer()
	registryv1.RegisterRegistryServiceServer(grpcServer, s)

	s.logger.Info("control server starting", zap.String("address", listener.Addr().String()))

	errChan := make(chan error, 1)
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		grpcServer.GracefulStop()
		return nil
	}
}

// Heartbeat acknowledges a probe by logging it and returning an empty response;
// it performs no liveness bookkeeping and always succeeds.
func (s *Server) Heartbeat(ctx context.Context, req *registryv1.HeartbeatRequest) (*registryv1.EmptyResponse, error) {
	s.logger.Debug("received heartbeat", zap.String("server_id", req.ServerId))
	return &registryv1.EmptyResponse{}, nil
}

// Stats reports this node's live session count against its configured capacity.
type Stats struct {
	ActiveSessions int32
	Capacity       int32
}
