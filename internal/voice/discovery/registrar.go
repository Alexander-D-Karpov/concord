package discovery

import (
	"context"
	"fmt"
	"time"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	registryv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/registry/v1"
	"github.com/Alexander-D-Karpov/concord/internal/version"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Registrar struct {
	client          registryv1.RegistryServiceClient
	logger          *zap.Logger
	serverID        string
	name            string
	region          string
	addrUDP         string
	addrCtrl        string
	capacity        int32
	heartbeatTicker *time.Ticker
	stopChan        chan struct{}
}

func NewRegistrar(
	registryURL string,
	serverID, name, region, addrUDP, addrCtrl string,
	capacity int32,
	logger *zap.Logger,
) (*Registrar, error) {
	conn, err := grpc.NewClient(registryURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to registry: %w", err)
	}

	client := registryv1.NewRegistryServiceClient(conn)

	return &Registrar{
		client:   client,
		logger:   logger,
		serverID: serverID,
		name:     name,
		region:   region,
		addrUDP:  addrUDP,
		addrCtrl: addrCtrl,
		capacity: capacity,
		stopChan: make(chan struct{}),
	}, nil
}

func (r *Registrar) Register(ctx context.Context) error {
	req := &registryv1.RegisterServerRequest{
		Server: &commonv1.VoiceServer{
			Id:           r.serverID,
			Name:         fmt.Sprintf("%s/v%s", r.name, version.Voice()),
			Region:       r.region,
			AddrUdp:      r.addrUDP,
			AddrCtrl:     r.addrCtrl,
			Status:       "online",
			CapacityHint: r.capacity,
			UpdatedAt:    timestamppb.Now(),
		},
	}
	resp, err := r.client.RegisterServer(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to register: %w", err)
	}

	r.serverID = resp.Server.Id

	r.logger.Info("registered with main API",
		zap.String("server_id", resp.Server.Id),
		zap.String("region", resp.Server.Region),
	)

	return nil
}

func (r *Registrar) StartHeartbeat(ctx context.Context, interval time.Duration, statsFunc func() (int32, int32, float64, float64)) {
	r.heartbeatTicker = time.NewTicker(interval)

	go func() {
		consecutiveFailures := 0
		for {
			select {
			case <-r.heartbeatTicker.C:
				activeRooms, activeSessions, cpu, outboundMbps := statsFunc()

				req := &registryv1.HeartbeatRequest{
					ServerId:       r.serverID,
					ActiveRooms:    activeRooms,
					ActiveSessions: activeSessions,
					Cpu:            cpu,
					OutboundMbps:   outboundMbps,
					Ts:             timestamppb.Now(),
				}

				_, err := r.client.Heartbeat(ctx, req)
				if err != nil {
					consecutiveFailures++
					r.logger.Warn("heartbeat failed", zap.Error(err), zap.Int("failures", consecutiveFailures))

					if consecutiveFailures >= 3 {
						r.logger.Info("re-registering after heartbeat failures")
						if regErr := r.Register(ctx); regErr != nil {
							r.logger.Error("re-registration failed", zap.Error(regErr))
						} else {
							consecutiveFailures = 0
						}
					}
				} else {
					consecutiveFailures = 0
				}
			case <-r.stopChan:
				return
			}
		}
	}()
}

func (r *Registrar) Stop() {
	if r.heartbeatTicker != nil {
		r.heartbeatTicker.Stop()
	}
	close(r.stopChan)
}
