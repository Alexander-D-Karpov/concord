package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"github.com/Alexander-D-Karpov/concord/internal/common/netinfo"
	"github.com/Alexander-D-Karpov/concord/internal/voice/control"
	"github.com/Alexander-D-Karpov/concord/internal/voice/discovery"
	"github.com/Alexander-D-Karpov/concord/internal/voice/health"
	"github.com/Alexander-D-Karpov/concord/internal/voice/room"
	"github.com/Alexander-D-Karpov/concord/internal/voice/router"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"github.com/Alexander-D-Karpov/concord/internal/voice/udp"
	"github.com/google/uuid"
)

const version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load(".env")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger, err := logging.Init(
		cfg.Logging.Level,
		cfg.Logging.Format,
		cfg.Logging.Output,
		cfg.Logging.EnableFile,
		cfg.Logging.FilePath,
	)
	if err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	defer func(logger *zap.Logger) {
		if err := logger.Sync(); err != nil {
			if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) {
				return
			}
			fmt.Fprintf(os.Stderr, "error syncing logger: %v\n", err)
		}
	}(logger)

	serverID := cfg.Voice.ServerID
	if serverID == "" {
		serverID = uuid.New().String()
		logger.Info("generated server ID", zap.String("server_id", serverID))
	}

	logger.Info("starting concord-voice",
		zap.String("version", version),
		zap.String("server_id", serverID),
		zap.String("region", cfg.Voice.Region),
	)

	// Initialize core components
	jwtManager := jwt.NewManager(cfg.Auth.JWTSecret, cfg.Auth.VoiceJWTSecret)
	sessionManager := session.NewManager()
	roomManager := room.NewManager()

	// Router needs access to both session and room state
	voiceRouter := router.NewRouter(sessionManager, logger)

	// Metrics and health
	metrics := telemetry.NewMetrics(logger)
	telemetryLogger := telemetry.NewLogger(logger)

	healthServer := health.NewServer(logger)
	healthServer.RegisterCheck("sessions", func(ctx context.Context) error {
		sessions := sessionManager.GetAllSessions()
		if len(sessions) > 10000 {
			return fmt.Errorf("too many sessions: %d", len(sessions))
		}
		return nil
	})
	healthServer.RegisterCheck("rooms", func(ctx context.Context) error {
		rooms := roomManager.GetAllRooms()
		if len(rooms) > 1000 {
			return fmt.Errorf("too many rooms: %d", len(rooms))
		}
		return nil
	})

	// Create UDP server
	udpPort := cfg.Voice.UDPPortStart
	udpServer, err := udp.NewServer(
		cfg.Voice.UDPHost,
		udpPort,
		sessionManager,
		voiceRouter,
		jwtManager,
		logger,
	)
	if err != nil {
		return fmt.Errorf("create UDP server: %w", err)
	}

	// Create control server for registry communication
	controlServer := control.NewServer(
		sessionManager,
		logger,
		serverID,
		cfg.Voice.Region,
		"concord-voice",
		1000,
	)

	// Compute advertised addresses
	ctx := context.Background()
	advertised := netinfo.ComputeAdvertised(
		ctx,
		cfg.Voice.PublicHost,
		cfg.Voice.UDPHost,
		udpPort,
	)
	netinfo.PrintAccessBanner(advertised, "Concord Voice Server")

	// Register with main API if configured
	var registrar *discovery.Registrar
	if cfg.Voice.RegistryURL != "" {
		publicAddr := advertised.PublicHost
		if publicAddr == "" {
			publicAddr = advertised.LANHost
		}

		registrar, err = discovery.NewRegistrar(
			cfg.Voice.RegistryURL,
			serverID,
			"concord-voice",
			cfg.Voice.Region,
			fmt.Sprintf("%s:%d", publicAddr, udpPort),
			fmt.Sprintf("%s:%d", publicAddr, cfg.Voice.ControlPort),
			1000,
			logger,
		)
		if err != nil {
			logger.Warn("failed to create registrar", zap.Error(err))
		} else {
			if err := registrar.Register(ctx); err != nil {
				logger.Warn("failed to register with main API", zap.Error(err))
			} else {
				logger.Info("registered with main API",
					zap.String("registry_url", cfg.Voice.RegistryURL),
				)
			}

			// Stats function for heartbeat
			statsFunc := func() (int32, int32, float64, float64) {
				rooms := roomManager.GetAllRooms()
				sessions := sessionManager.GetAllSessions()

				// CPU estimation
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				cpu := float64(runtime.NumGoroutine()) / 100.0

				// Outbound bandwidth
				stats := metrics.GetStats()
				outboundMbps := float64(stats.BytesSent) / (1024 * 1024)

				return int32(len(rooms)), int32(len(sessions)), cpu, outboundMbps
			}

			registrar.StartHeartbeat(ctx, 30*time.Second, statsFunc)
		}
	}

	// Create cancellable context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 4)

	// Start UDP server
	go func() {
		logger.Info("starting UDP server", zap.Int("port", udpPort))
		if err := udpServer.Start(ctx); err != nil {
			errChan <- fmt.Errorf("UDP server: %w", err)
		}
	}()

	// Start control server
	go func() {
		logger.Info("starting control server", zap.Int("port", cfg.Voice.ControlPort))
		if err := controlServer.Start(ctx, cfg.Voice.ControlPort); err != nil {
			errChan <- fmt.Errorf("control server: %w", err)
		}
	}()

	// Start metrics server
	go func() {
		logger.Info("starting metrics server", zap.Int("port", 9101))
		if err := metrics.Start(ctx, 9101, "/metrics"); err != nil {
			errChan <- fmt.Errorf("metrics server: %w", err)
		}
	}()

	// Start health server
	go func() {
		logger.Info("starting health server", zap.Int("port", 8082))
		if err := healthServer.Start(ctx, 8082, "/health"); err != nil {
			errChan <- fmt.Errorf("health server: %w", err)
		}
	}()

	// Background cleanup and stats goroutine
	go func() {
		cleanupTicker := time.NewTicker(30 * time.Second)
		statsTicker := time.NewTicker(10 * time.Second)
		defer cleanupTicker.Stop()
		defer statsTicker.Stop()

		for {
			select {
			case <-cleanupTicker.C:
				// Clean up inactive sessions (2 minute timeout)
				removed := sessionManager.CleanupInactive(2 * time.Minute)
				if len(removed) > 0 {
					logger.Info("cleaned up inactive sessions",
						zap.Int("count", len(removed)),
					)

					// Update room states
					for _, sessionID := range removed {
						// Room cleanup would happen here if needed
						telemetryLogger.LogSessionEnded(sessionID, "", "")
					}
				}

			case <-statsTicker.C:
				// Update metrics
				sessions := sessionManager.GetAllSessions()
				rooms := roomManager.GetAllRooms()

				metrics.SetActiveSessions(int32(len(sessions)))
				metrics.SetActiveRooms(int32(len(rooms)))

				// Log stats periodically
				stats := metrics.GetStats()
				logger.Debug("server stats",
					zap.Int("active_sessions", len(sessions)),
					zap.Int("active_rooms", len(rooms)),
					zap.Uint64("packets_received", stats.PacketsReceived),
					zap.Uint64("packets_sent", stats.PacketsSent),
					zap.Uint64("bytes_received", stats.BytesReceived),
					zap.Uint64("bytes_sent", stats.BytesSent),
					zap.Uint64("packets_dropped", stats.PacketsDropped),
				)

			case <-ctx.Done():
				return
			}
		}
	}()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errChan:
		logger.Error("server error", zap.Error(err))
		return err
	case sig := <-sigChan:
		logger.Info("received shutdown signal", zap.String("signal", sig.String()))
	}

	// Graceful shutdown
	logger.Info("shutting down gracefully...")

	// Stop accepting new connections
	cancel()

	// Stop router
	voiceRouter.Stop()

	// Stop registrar
	if registrar != nil {
		registrar.Stop()
	}

	// Give goroutines time to finish
	time.Sleep(2 * time.Second)

	logger.Info("shutdown complete")
	return nil
}
