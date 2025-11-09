package registry

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type Service struct {
	repo   *Repository
	logger *zap.Logger
}

func NewService(pool *pgxpool.Pool, logger *zap.Logger) *Service {
	return &Service{
		repo:   NewRepository(pool),
		logger: logger,
	}
}

func (s *Service) RegisterServer(ctx context.Context, server *VoiceServer) (*VoiceServer, error) {
	if err := s.repo.Upsert(ctx, server); err != nil {
		s.logger.Error("failed to register server", zap.Error(err))
		return nil, err
	}

	s.logger.Info("voice server registered",
		zap.String("server_id", server.ID.String()),
		zap.String("name", server.Name),
		zap.String("region", server.Region),
	)

	return server, nil
}

func (s *Service) Heartbeat(ctx context.Context, serverID uuid.UUID, activeRooms, activeSessions int32, cpu, outboundMbps float64) error {
	if err := s.repo.UpdateHeartbeat(ctx, serverID, activeRooms, activeSessions, cpu, outboundMbps); err != nil {
		s.logger.Warn("failed to update heartbeat",
			zap.String("server_id", serverID.String()),
			zap.Error(err),
		)
		return err
	}

	//s.logger.Debug("heartbeat received",
	//	zap.String("server_id", serverID.String()),
	//	zap.Int32("active_sessions", activeSessions),
	//)

	return nil
}

func (s *Service) ListServers(ctx context.Context, region *string) ([]*VoiceServer, error) {
	return s.repo.List(ctx, region)
}
