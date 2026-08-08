package registry

import (
	"context"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Service holds the registry business logic over a Repository, coordinating server
// registration, secret verification, heartbeats, and listing.
type Service struct {
	repo   *Repository
	logger *zap.Logger
}

// NewService constructs a Service with a Repository over the given pool.
func NewService(pool *pgxpool.Pool, logger *zap.Logger) *Service {
	return &Service{
		repo:   NewRepository(pool),
		logger: logger,
	}
}

// RegisterServer upserts the server, hashing a non-empty plaintextSecret before
// storage (an empty secret leaves any existing hash untouched). Returns the stored
// server with timestamps populated, or Internal on failure.
func (s *Service) RegisterServer(ctx context.Context, server *VoiceServer, plaintextSecret string) (*VoiceServer, error) {
	if plaintextSecret != "" {
		h := HashSecret(plaintextSecret)
		server.SecretHash = &h
	}

	if err := s.repo.Upsert(ctx, server); err != nil {
		s.logger.Error("failed to register server", zap.Error(err))
		return nil, errors.Internal("failed to register server", err)
	}

	s.logger.Info("voice server registered",
		zap.String("server_id", server.ID.String()),
		zap.String("name", server.Name),
		zap.String("region", server.Region),
	)

	return server, nil
}

// VerifyServerSecret parses serverID and delegates to the repository's constant-time
// secret check. Returns BadRequest if serverID is not a valid UUID. Implements
// SecretVerifier for MachineAuthInterceptor.
func (s *Service) VerifyServerSecret(ctx context.Context, serverID, secret string) error {
	id, err := uuid.Parse(serverID)
	if err != nil {
		return errors.BadRequest("invalid server id")
	}
	return s.repo.VerifyServerSecret(ctx, id, secret)
}

// Heartbeat validates that all metrics are non-negative (BadRequest otherwise) and
// updates the server's load and liveness. A missing server surfaces as NotFound.
func (s *Service) Heartbeat(ctx context.Context, serverID uuid.UUID, activeRooms, activeSessions int32, cpu, outboundMbps float64) error {
	if activeRooms < 0 || activeSessions < 0 || cpu < 0 || outboundMbps < 0 {
		return errors.BadRequest("heartbeat metrics must be non-negative")
	}

	if err := s.repo.UpdateHeartbeat(ctx, serverID, activeRooms, activeSessions, cpu, outboundMbps); err != nil {
		s.logger.Warn("failed to update heartbeat",
			zap.String("server_id", serverID.String()),
			zap.Error(err),
		)
		return errors.NotFound("voice server not found")
	}

	return nil
}

// ListServers returns online servers ordered least-loaded first, optionally filtered
// by region (nil for all regions).
func (s *Service) ListServers(ctx context.Context, region *string) ([]*VoiceServer, error) {
	return s.repo.List(ctx, region)
}
