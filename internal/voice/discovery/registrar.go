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
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Metadata keys the API's registry.MachineAuthInterceptor expects. Kept as
// local literals so the voice binary doesn't import the API-side registry
// package; registrar_test.go asserts they match registry's exported keys.
const (
	secretMetadataKey   = "x-voice-secret"
	serverIDMetadataKey = "x-voice-server-id"
)

// Registrar registers this voice node with the main API's registry and keeps it
// alive with periodic heartbeats. It holds the gRPC client plus the node's
// identity/placement fields and two secrets: registerSecret (fleet-wide, for the
// initial register) and serverSecret (this node's own, presented on heartbeats).
// The heartbeat loop runs in a background goroutine started by StartHeartbeat.
type Registrar struct {
	client         registryv1.RegistryServiceClient
	logger         *zap.Logger
	serverID       string
	name           string
	region         string
	addrUDP        string
	addrCtrl       string
	capacity       int32
	registerSecret string
	serverSecret   string

	heartbeatTicker *time.Ticker
	stopChan        chan struct{}
}

// NewRegistrar dials registryURL (insecure transport by default; override via
// dialOpts) and returns a Registrar ready to Register. Dialing is lazy — an
// unreachable registry surfaces on the first RPC, not here. Returns an error
// only if constructing the client fails.
func NewRegistrar(
	registryURL string,
	serverID, name, region, addrUDP, addrCtrl string,
	capacity int32,
	registerSecret, serverSecret string,
	logger *zap.Logger,
	dialOpts ...grpc.DialOption,
) (*Registrar, error) {
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, dialOpts...)
	conn, err := grpc.NewClient(registryURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to registry: %w", err)
	}

	client := registryv1.NewRegistryServiceClient(conn)

	return &Registrar{
		client:         client,
		logger:         logger,
		serverID:       serverID,
		name:           name,
		region:         region,
		addrUDP:        addrUDP,
		addrCtrl:       addrCtrl,
		capacity:       capacity,
		registerSecret: registerSecret,
		serverSecret:   serverSecret,
		stopChan:       make(chan struct{}),
	}, nil
}

// registerCtx attaches the shared registration secret the API compares against
// its VOICE_REGISTER_SECRET.
func (r *Registrar) registerCtx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, secretMetadataKey, r.registerSecret)
}

// heartbeatCtx attaches this server's own secret (verified against the stored
// secret_hash) plus its id.
func (r *Registrar) heartbeatCtx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx,
		secretMetadataKey, r.serverSecret,
		serverIDMetadataKey, r.serverID,
	)
}

// Register announces this node to the API (name suffixed with the build
// version) and adopts the server id the registry returns, so a registry-assigned
// id overrides the local one for subsequent heartbeats. Also doubles as the
// recovery path when heartbeats repeatedly fail.
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
		// Stored (hashed) as this server's secret_hash; presented on every heartbeat.
		SharedSecret: r.serverSecret,
	}
	resp, err := r.client.RegisterServer(r.registerCtx(ctx), req)
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

// sendHeartbeat reports one liveness+load sample. statsFunc supplies, in order,
// active rooms, active sessions, CPU fraction (0..1), and outbound Mbps, which
// the registry uses for load-based placement. Returns the RPC error.
func (r *Registrar) sendHeartbeat(ctx context.Context, statsFunc func() (int32, int32, float64, float64)) error {
	activeRooms, activeSessions, cpu, outboundMbps := statsFunc()

	req := &registryv1.HeartbeatRequest{
		ServerId:       r.serverID,
		ActiveRooms:    activeRooms,
		ActiveSessions: activeSessions,
		Cpu:            cpu,
		OutboundMbps:   outboundMbps,
		Ts:             timestamppb.Now(),
	}

	_, err := r.client.Heartbeat(r.heartbeatCtx(ctx), req)
	return err
}

// StartHeartbeat spawns a goroutine that sends a heartbeat every interval until
// ctx is cancelled or Stop is called. After 3 consecutive failures it attempts a
// full re-registration (resetting the failure count on success), so a node that
// the registry dropped can rejoin without a restart. Non-blocking.
func (r *Registrar) StartHeartbeat(ctx context.Context, interval time.Duration, statsFunc func() (int32, int32, float64, float64)) {
	r.heartbeatTicker = time.NewTicker(interval)

	go func() {
		consecutiveFailures := 0
		for {
			select {
			case <-r.heartbeatTicker.C:
				if err := r.sendHeartbeat(ctx, statsFunc); err != nil {
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

// Stop halts the heartbeat ticker and signals the loop to exit by closing
// stopChan. Not safe to call more than once (the close would panic).
func (r *Registrar) Stop() {
	if r.heartbeatTicker != nil {
		r.heartbeatTicker.Stop()
	}
	close(r.stopChan)
}
