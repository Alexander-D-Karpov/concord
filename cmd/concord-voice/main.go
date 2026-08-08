package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/version"
	"github.com/Alexander-D-Karpov/concord/internal/voice/status"
	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"github.com/Alexander-D-Karpov/concord/internal/common/netinfo"
	"github.com/Alexander-D-Karpov/concord/internal/voice/congestion"
	"github.com/Alexander-D-Karpov/concord/internal/voice/control"
	"github.com/Alexander-D-Karpov/concord/internal/voice/discovery"
	"github.com/Alexander-D-Karpov/concord/internal/voice/health"
	"github.com/Alexander-D-Karpov/concord/internal/voice/room"
	"github.com/Alexander-D-Karpov/concord/internal/voice/router"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/tcp"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"github.com/Alexander-D-Karpov/concord/internal/voice/udp"
	"github.com/google/uuid"
)

// main runs the voice media server, printing any fatal error to stderr and exiting
// non-zero.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// run is the composition root for concord-voice. It loads config/.env and logging,
// derives (or generates) the server ID, and builds the session/room managers,
// telemetry, congestion controller and packet router. It opens the UDP server pool
// (multi-port, or single-port SO_REUSEPORT when configured), an optional TCP/TLS media
// fallback, and the control server; computes advertised addresses and, when a registry
// URL is set, registers and heartbeats CPU/egress stats to the main API. It starts the
// UDP, control, metrics (9101) and health (8082) servers plus a periodic loop that
// steps congestion tiers, sweeps inactive sessions and paints stats (sticky TTY status
// bar or structured log line). It then blocks until a signal or server error and shuts
// down by cancelling the context, stopping the router and registrar, and draining.
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
		zap.String("version", version.Voice()),
		zap.String("server_id", serverID),
		zap.String("region", cfg.Voice.Region),
	)

	// Initialize core components
	jwtManager := jwt.NewManager(cfg.Auth.JWTSecret, cfg.Auth.VoiceJWTSecret)
	sessionManager := session.NewManager()
	roomManager := room.NewManager()

	// Metrics and health
	metrics := telemetry.NewMetrics(logger)
	telemetryLogger := telemetry.NewLogger(logger)

	congestionCtrl := congestion.NewController(congestion.DefaultConfig())
	voiceRouter := router.NewRouter(sessionManager, logger, metrics, congestionCtrl)

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

	// Create UDP server pool
	udpPort := cfg.Voice.UDPPortStart
	portCount := cfg.Voice.UDPPortCount
	if portCount <= 0 {
		portCount = 50
	}
	if portCount > (cfg.Voice.UDPPortEnd - cfg.Voice.UDPPortStart) {
		portCount = cfg.Voice.UDPPortEnd - cfg.Voice.UDPPortStart
	}

	singlePort := cfg.Voice.SinglePort
	udpStartPort := cfg.Voice.UDPPortStart
	if singlePort {
		if cfg.Voice.PublicUDPPort > 0 {
			udpStartPort = cfg.Voice.PublicUDPPort
		}
		udpPort = udpStartPort
		portCount = cfg.Voice.SocketCount
		if portCount <= 0 {
			portCount = runtime.NumCPU()
		}
		logger.Info("single-port UDP mode (SO_REUSEPORT)",
			zap.Int("port", udpStartPort),
			zap.Int("sockets", portCount),
		)
	}

	udpPool, err := udp.NewServerPool(
		cfg.Voice.UDPHost,
		udpStartPort,
		portCount,
		singlePort,
		sessionManager,
		voiceRouter,
		jwtManager,
		logger,
		metrics,
		congestionCtrl,
	)
	if err != nil {
		return fmt.Errorf("create UDP pool: %w", err)
	}
	// TCP-fallback media has no origin UDP socket; give the router a default
	// egress socket so it can still forward to UDP peers.
	voiceRouter.SetDefaultConn(udpPool.PrimaryConn())

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

	statusSrv := status.NewServer(sessionManager, jwtManager, metrics, logger)
	go func() {
		err := statusSrv.Start(ctx, cfg.Voice.StatusPort)
		if err != nil {
			logger.Error("status server error", zap.Error(err))
		}
	}()

	var registrar *discovery.Registrar
	if cfg.Voice.RegistryURL != "" {
		publicAddr := advertised.PublicHost
		if publicAddr == "" {
			publicAddr = advertised.LANHost
		}

		// Use the port from advertised, don't append again
		udpAddress := fmt.Sprintf("%s:%d", publicAddr, advertised.Port)
		ctrlAddress := fmt.Sprintf("%s:%d", publicAddr, cfg.Voice.ControlPort)

		registrar, err = discovery.NewRegistrar(
			cfg.Voice.RegistryURL,
			serverID,
			"concord-voice",
			cfg.Voice.Region,
			udpAddress,
			ctrlAddress,
			1000,
			cfg.Voice.RegisterSecret,
			cfg.Voice.Secret,
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

			// Stats function for heartbeat: real CPU utilization + egress rate.
			loadSampler := telemetry.NewLoadSampler()
			statsFunc := func() (int32, int32, float64, float64) {
				rooms := sessionManager.GetAllRooms()
				sessions := sessionManager.GetAllSessions()
				stats := metrics.GetStats()
				cpu, mbps := loadSampler.Sample(stats.BytesSent, time.Now())
				return int32(len(rooms)), int32(len(sessions)), cpu, mbps
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
		logger.Info("starting UDP pool", zap.Int("start_port", cfg.Voice.UDPPortStart), zap.Int("count", portCount))
		if err := udpPool.Start(ctx); err != nil {
			errChan <- fmt.Errorf("UDP pool: %w", err)
		}
	}()

	// Optional TCP/TLS fallback for clients whose UDP is blocked
	if cfg.Voice.TCPPort > 0 {
		var tlsCfg *tls.Config
		if cfg.Voice.TLSCert != "" && cfg.Voice.TLSKey != "" {
			cert, certErr := tls.LoadX509KeyPair(cfg.Voice.TLSCert, cfg.Voice.TLSKey)
			if certErr != nil {
				return fmt.Errorf("load voice TLS keypair: %w", certErr)
			}
			tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}}
		}
		tcpSrv := tcp.NewServer(udpPool.Handler(), sessionManager, logger, tlsCfg)
		go func() {
			if err := tcpSrv.Start(ctx, cfg.Voice.TCPPort); err != nil {
				errChan <- fmt.Errorf("tcp fallback: %w", err)
			}
		}()
	}

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

	go func() {
		cleanupTicker := time.NewTicker(10 * time.Second)
		statsTicker := time.NewTicker(10 * time.Second)
		tierTicker := time.NewTicker(time.Second)
		defer cleanupTicker.Stop()
		defer statsTicker.Stop()
		defer tierTicker.Stop()

		// A live bottom status bar refreshes faster than the scrolling log line.
		if logging.StatusEnabled() {
			statsTicker.Reset(2 * time.Second)
		}

		statsLoad := telemetry.NewLoadSampler()

		for {
			select {
			case <-tierTicker.C:
				congestionCtrl.StepTiers(time.Now())
			case <-cleanupTicker.C:
				removed := udpPool.SweepInactive(20*time.Second, 90*time.Second)
				congestionCtrl.Prune(time.Now())
				if len(removed) > 0 {
					logger.Info("cleaned up inactive sessions",
						zap.Int("count", len(removed)),
					)

					for _, sessionID := range removed {
						telemetryLogger.LogSessionEnded(sessionID, "", "")
					}
				}

			case <-statsTicker.C:
				now := time.Now()
				sessions := sessionManager.GetAllSessions()
				rooms := sessionManager.GetAllRooms() // session manager's room tracking
				metrics.SetActiveSessions(int32(len(sessions)))
				metrics.SetActiveRooms(int32(len(rooms)))

				snap := metrics.Snapshot(now, 3)
				cpu, _ := statsLoad.Sample(snap.TotalBytesOut, now)

				// Interactive TTY: paint a sticky bottom bar (logs scroll above).
				if logging.StatusEnabled() {
					bar := "voice │ " + snap.Summary(cpu, logging.StatusColor())
					if snap.HasEvents() {
						bar += " │ " + snap.Reliability()
					}
					if len(snap.TopRooms) > 0 {
						bar += " │ " + formatTopRooms(snap.TopRooms)
					}
					logging.SetStatus(bar)
					continue
				}

				// Non-interactive (piped/JSON/file): emit a structured line.
				fields := []zap.Field{
					zap.Float64("mbps_out", snap.MbpsOut),
					zap.Float64("drop_ratio", snap.DropRatio),
					zap.Float64("rtt_p95_ms", snap.RTTp95),
				}
				if snap.HasEvents() {
					fields = append(fields, zap.String("reliability", snap.Reliability()))
				}
				if len(snap.TopRooms) > 0 {
					fields = append(fields, zap.String("top_rooms", formatTopRooms(snap.TopRooms)))
				}
				msg := "voice │ " + snap.Summary(cpu, logging.ColorEnabled())
				if len(sessions) > 0 || snap.PktsInPerSec > 0 || snap.PktsOutPerSec > 0 {
					logger.Info(msg, fields...)
				} else {
					logger.Debug(msg, fields...)
				}

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

	// Graceful shutdown: drop the status bar so the shell prompt returns clean.
	logging.ClearStatus()
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

// formatTopRooms renders the hottest rooms by routed volume for the stats line,
// truncating room IDs so the line stays terminal-width friendly.
func formatTopRooms(rooms []telemetry.RoomStat) string {
	var b strings.Builder
	for i, r := range rooms {
		if i > 0 {
			b.WriteByte(' ')
		}
		id := r.Room
		if len(id) > 8 {
			id = id[:8]
		}
		fmt.Fprintf(&b, "%s=%.1fMB", id, float64(r.Bytes)/1e6)
	}
	return b.String()
}
