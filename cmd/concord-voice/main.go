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
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

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
	}

	logger.Info("starting concord-voice",
		zap.String("version", "0.1.0"),
		zap.String("server_id", serverID),
		zap.String("udp_host", cfg.Voice.UDPHost),
		zap.Int("udp_port_start", cfg.Voice.UDPPortStart),
		zap.String("region", cfg.Voice.Region),
	)

	jwtManager := jwt.NewManager(cfg.Auth.JWTSecret, cfg.Auth.VoiceJWTSecret)

	sessionManager := session.NewManager()
	roomManager := room.NewManager()
	voiceRouter := router.NewRouter(sessionManager, logger)

	metrics := telemetry.NewMetrics(logger)
	healthServer := health.NewServer(logger)

	healthServer.RegisterCheck("sessions", func(ctx context.Context) error {
		if len(sessionManager.GetAllSessions()) > 10000 {
			return fmt.Errorf("too many sessions")
		}
		return nil
	})

	udpServer, err := udp.NewServer(
		cfg.Voice.UDPHost,
		cfg.Voice.UDPPortStart,
		sessionManager,
		voiceRouter,
		jwtManager,
		logger,
	)
	if err != nil {
		return fmt.Errorf("create UDP server: %w", err)
	}

	controlServer := control.NewServer(
		sessionManager,
		logger,
		serverID,
		cfg.Voice.Region,
		"voice-server-1",
		1000,
	)

	ctx := context.Background()
	ad := netinfo.ComputeAdvertised(ctx,
		cfg.Voice.PublicHost,
		cfg.Voice.UDPHost,
		cfg.Voice.UDPPortStart,
	)
	netinfo.PrintAccessBanner(ad, "Concord Voice is ready")

	var registrar *discovery.Registrar
	if cfg.Voice.RegistryURL != "" {
		registrar, err = discovery.NewRegistrar(
			cfg.Voice.RegistryURL,
			serverID,
			"voice-server-1",
			cfg.Voice.Region,
			fmt.Sprintf("%s:%d", ad.PublicHost, cfg.Voice.UDPPortStart),
			fmt.Sprintf("%s:%d", ad.PublicHost, cfg.Voice.ControlPort),
			1000,
			logger,
		)
		if err != nil {
			logger.Warn("failed to create registrar", zap.Error(err))
		} else {
			if err := registrar.Register(ctx); err != nil {
				logger.Warn("failed to register with main API", zap.Error(err))
			}

			statsFunc := func() (int32, int32, float64, float64) {
				rooms := roomManager.GetAllRooms()
				sessions := sessionManager.GetAllSessions()

				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				cpu := float64(runtime.NumGoroutine()) / 100.0

				stats := metrics.GetStats()
				outboundMbps := float64(stats.BytesSent) / 1024 / 1024

				return int32(len(rooms)), int32(len(sessions)), cpu, outboundMbps
			}

			registrar.StartHeartbeat(ctx, 30*time.Second, statsFunc)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 4)

	go func() {
		if err := udpServer.Start(ctx); err != nil {
			errChan <- fmt.Errorf("UDP server error: %w", err)
		}
	}()

	go func() {
		if err := controlServer.Start(ctx, cfg.Voice.ControlPort); err != nil {
			errChan <- fmt.Errorf("control server error: %w", err)
		}
	}()

	go func() {
		if err := metrics.Start(ctx, 9092, "/metrics"); err != nil {
			errChan <- fmt.Errorf("metrics server error: %w", err)
		}
	}()

	go func() {
		if err := healthServer.Start(ctx, 9093, "/health"); err != nil {
			errChan <- fmt.Errorf("health server error: %w", err)
		}
	}()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				removed := sessionManager.CleanupInactive(2 * time.Minute)
				if len(removed) > 0 {
					logger.Info("cleaned up inactive sessions", zap.Int("count", len(removed)))
				}

				sessions := sessionManager.GetAllSessions()
				rooms := roomManager.GetAllRooms()
				metrics.SetActiveSessions(int32(len(sessions)))
				metrics.SetActiveRooms(int32(len(rooms)))

			case <-ctx.Done():
				return
			}
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errChan:
		return err
	case sig := <-sigChan:
		logger.Info("received shutdown signal", zap.String("signal", sig.String()))
	}

	logger.Info("shutting down gracefully...")
	cancel()
	voiceRouter.Stop()
	if registrar != nil {
		registrar.Stop()
	}

	logger.Info("shutdown complete")
	return nil
}
