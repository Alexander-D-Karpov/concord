package control

import (
	"context"
	"fmt"
	"net"

	registryv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/registry/v1"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type Server struct {
	registryv1.UnimplementedRegistryServiceServer
	sessionManager *session.Manager
	logger         *zap.Logger
	serverID       string
	region         string
	name           string
	capacity       int32
}

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
		name:           name,
		capacity:       capacity,
	}
}

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

func (s *Server) Heartbeat(ctx context.Context, req *registryv1.HeartbeatRequest) (*registryv1.EmptyResponse, error) {
	s.logger.Debug("received heartbeat", zap.String("server_id", req.ServerId))
	return &registryv1.EmptyResponse{}, nil
}

func (s *Server) GetStats(ctx context.Context) *Stats {
	return &Stats{
		ActiveSessions: int32(len(s.sessionManager.GetAllSessions())),
		Capacity:       s.capacity,
	}
}

type Stats struct {
	ActiveSessions int32
	Capacity       int32
}
