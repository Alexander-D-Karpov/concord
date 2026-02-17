package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/readtracking"
	"github.com/Alexander-D-Karpov/concord/internal/security"
	"github.com/Alexander-D-Karpov/concord/internal/swagger"
	"github.com/Alexander-D-Karpov/concord/internal/typing"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	adminv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/admin/v1"
	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	chatv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/chat/v1"
	dmv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/dm/v1"
	friendsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/friends/v1"
	membershipv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/membership/v1"
	registryv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/registry/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	usersv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/users/v1"
	"github.com/Alexander-D-Karpov/concord/internal/admin"
	authsvc "github.com/Alexander-D-Karpov/concord/internal/auth"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/auth/oauth"
	"github.com/Alexander-D-Karpov/concord/internal/call"
	"github.com/Alexander-D-Karpov/concord/internal/chat"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"github.com/Alexander-D-Karpov/concord/internal/dm"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/friends"
	"github.com/Alexander-D-Karpov/concord/internal/gateway"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/infra/migrations"
	"github.com/Alexander-D-Karpov/concord/internal/membership"
	"github.com/Alexander-D-Karpov/concord/internal/middleware"
	"github.com/Alexander-D-Karpov/concord/internal/observability"
	"github.com/Alexander-D-Karpov/concord/internal/ratelimit"
	"github.com/Alexander-D-Karpov/concord/internal/registry"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/storage"
	"github.com/Alexander-D-Karpov/concord/internal/stream"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
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
	defer func() {
		_ = logger.Sync()
	}()

	logger.Info("starting concord-api",
		zap.String("version", version),
		zap.Int("grpc_port", cfg.Server.GRPCPort),
	)

	if err := generateOpenAPISpec(logger); err != nil {
		logger.Warn("failed to generate OpenAPI spec", zap.Error(err))
	}

	database, err := db.New(cfg.Database)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer database.Close()

	logger.Info("connected to database")

	ctx := context.Background()

	if err := migrations.Run(ctx, database.Pool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	logger.Info("migrations applied successfully")

	var cacheClient *cache.Cache
	if cfg.Redis.Enabled {
		cacheClient, err = cache.New(
			cfg.Redis.Host,
			cfg.Redis.Port,
			cfg.Redis.Password,
			cfg.Redis.DB,
		)
		if err != nil {
			logger.Warn("failed to connect to Redis, continuing without cache", zap.Error(err))
		} else {
			defer func() {
				if err := cacheClient.Close(); err != nil {
					logger.Error("failed to close cache", zap.Error(err))
				}
			}()
			logger.Info("connected to Redis")
		}
	}

	var aside *cache.AsidePattern
	if cacheClient != nil {
		aside = cache.NewAsidePattern(cacheClient)
	}

	metrics := observability.NewMetrics(logger)
	healthChecker := observability.NewHealthChecker(logger, version)

	healthChecker.RegisterCheck("database", func(ctx context.Context) (observability.HealthStatus, string, error) {
		if err := database.Health(ctx); err != nil {
			return observability.StatusUnhealthy, "database connection failed", err
		}
		return observability.StatusHealthy, "database connection ok", nil
	})

	if cacheClient != nil {
		healthChecker.RegisterCheck("redis", func(ctx context.Context) (observability.HealthStatus, string, error) {
			if err := cacheClient.Ping(ctx); err != nil {
				return observability.StatusDegraded, "redis connection failed", err
			}
			return observability.StatusHealthy, "redis connection ok", nil
		})
	}

	jwtManager := jwt.NewManager(cfg.Auth.JWTSecret, cfg.Auth.VoiceJWTSecret)
	authInterceptor := interceptor.NewAuthInterceptor(jwtManager)

	var rateLimiter *ratelimit.Limiter
	if cfg.RateLimit.Enabled {
		rateLimiter = ratelimit.NewLimiter(
			cacheClient,
			cfg.RateLimit.RequestsPerMinute,
			cfg.RateLimit.Burst,
			true,
		)
		logger.Info("rate limiting enabled")
	} else {
		rateLimiter = ratelimit.NewLimiter(nil, 500, 100, false)
	}
	rateLimitInterceptor := ratelimit.NewInterceptor(rateLimiter)

	storageService, err := storage.New(cfg.Storage.Path, cfg.Storage.URL, logger)
	if err != nil {
		return fmt.Errorf("init storage: %w", err)
	}

	storageHandler := storage.NewHandler(storageService, logger)

	snowflakeGen := infra.NewSnowflakeGenerator(1)
	usersRepo := users.NewRepository(database.Pool)
	if cacheClient != nil {
		usersRepo = users.NewRepositoryWithCache(database.Pool, cacheClient)
	}
	eventsHub := events.NewHub(logger, database.Pool, aside)

	usersService := users.NewService(usersRepo, eventsHub)
	usersHandler := users.NewHandler(usersService)

	roomsRepo := rooms.NewRepository(database.Pool)
	if cacheClient != nil {
		roomsRepo = rooms.NewRepositoryWithCache(database.Pool, cacheClient)
	}
	roomsService := rooms.NewService(roomsRepo, eventsHub, aside)
	roomsHandler := rooms.NewHandler(roomsService)

	readTrackingRepo := readtracking.NewRepository(database.Pool)
	readTrackingSvc := readtracking.NewService(readTrackingRepo, eventsHub)

	typingRepo := typing.NewRepository(database.Pool)
	typingSvc := typing.NewService(typingRepo, eventsHub, usersRepo)

	messagesRepo := chat.NewRepository(database.Pool, snowflakeGen)
	chatService := chat.NewService(messagesRepo, roomsRepo, eventsHub, aside)
	chatHandler := chat.NewHandler(chatService, storageService, readTrackingSvc, typingSvc)

	membershipService := membership.NewService(roomsRepo, eventsHub, aside)
	membershipHandler := membership.NewHandler(membershipService)

	streamHandler := stream.NewHandler(eventsHub, usersRepo)

	voiceAssignService := voiceassign.NewService(database.Pool, jwtManager, cacheClient)
	callHandler := call.NewHandler(voiceAssignService, roomsRepo, eventsHub, logger)

	go voiceAssignService.StartHealthChecker(ctx, 30*time.Second)

	friendsRepo := friends.NewRepository(database.Pool)
	if cacheClient != nil {
		friendsRepo = friends.NewRepositoryWithCache(database.Pool, cacheClient)
	}
	friendsService := friends.NewService(friendsRepo, eventsHub, usersRepo)
	friendsHandler := friends.NewHandler(friendsService)

	adminService := admin.NewService(database.Pool, roomsRepo, eventsHub, logger)
	adminHandler := admin.NewHandler(adminService)

	dmRepo := dm.NewRepository(database.Pool)
	dmMsgRepo := dm.NewMessageRepository(database.Pool, snowflakeGen)
	dmService := dm.NewService(dmRepo, dmMsgRepo, usersRepo, eventsHub, voiceAssignService, logger)
	dmHandler := dm.NewHandler(dmService, storageService)
	dmHandler.SetReadTrackingService(readTrackingSvc)
	dmHandler.SetTypingService(typingSvc)

	var oauthManager *oauth.Manager
	if len(cfg.Auth.OAuth) > 0 {
		oauthManager = oauth.NewManager(cfg.Auth)
		logger.Info("OAuth providers configured", zap.Int("count", len(cfg.Auth.OAuth)))
	}

	authService := authsvc.NewService(
		usersRepo,
		database.Pool,
		jwtManager,
		oauthManager,
		cacheClient,
		cfg.Auth,
	)
	authHandler := authsvc.NewHandler(authService)

	registryService := registry.NewService(database.Pool, logger)
	registryHandler := registry.NewHandler(registryService)

	interceptors := []grpc.UnaryServerInterceptor{
		middleware.RecoveryInterceptor(logger),
		observability.RequestIDInterceptor(logger),
		metrics.UnaryServerInterceptor(),
		middleware.TimeoutInterceptor(60 * time.Second),
		rateLimitInterceptor.Unary(),
		authInterceptor.Unary(),
	}

	streamInterceptors := []grpc.StreamServerInterceptor{
		middleware.StreamRecoveryInterceptor(logger),
		metrics.StreamServerInterceptor(),
		rateLimitInterceptor.Stream(),
		authInterceptor.Stream(),
	}

	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(interceptors...),
		grpc.ChainStreamInterceptor(streamInterceptors...),
		grpc.MaxRecvMsgSize(16 * 1024 * 1024),
		grpc.MaxSendMsgSize(16 * 1024 * 1024),
	}

	if cfg.Server.TLSCertFile != "" && cfg.Server.TLSKeyFile != "" {
		tlsCfg, err := security.ServerTLSConfig(cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("init TLS: %w", err)
		}

		serverOpts = append(serverOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
		logger.Info("gRPC server TLS enabled",
			zap.String("cert", cfg.Server.TLSCertFile),
		)
	}

	grpcServer := grpc.NewServer(serverOpts...)

	authv1.RegisterAuthServiceServer(grpcServer, authHandler)
	usersv1.RegisterUsersServiceServer(grpcServer, usersHandler)
	roomsv1.RegisterRoomsServiceServer(grpcServer, roomsHandler)
	chatv1.RegisterChatServiceServer(grpcServer, chatHandler)
	membershipv1.RegisterMembershipServiceServer(grpcServer, membershipHandler)
	streamv1.RegisterStreamServiceServer(grpcServer, streamHandler)
	callv1.RegisterCallServiceServer(grpcServer, callHandler)
	registryv1.RegisterRegistryServiceServer(grpcServer, registryHandler)
	friendsv1.RegisterFriendsServiceServer(grpcServer, friendsHandler)
	adminv1.RegisterAdminServiceServer(grpcServer, adminHandler)
	dmv1.RegisterDMServiceServer(grpcServer, dmHandler)
	reflection.Register(grpcServer)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Server.GRPCPort))
	if err != nil {
		return fmt.Errorf("create listener: %w", err)
	}

	logger.Info("gRPC server listening", zap.String("address", listener.Addr().String()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 5)

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			errChan <- fmt.Errorf("serve grpc: %w", err)
		}
	}()

	go func() {
		if err := metrics.Start(ctx, 9100); err != nil {
			errChan <- fmt.Errorf("metrics server: %w", err)
		}
	}()

	go func() {
		if err := healthChecker.Start(ctx, 8081); err != nil {
			errChan <- fmt.Errorf("health server: %w", err)
		}
	}()

	httpGateway := gateway.New(fmt.Sprintf("localhost:%d", cfg.Server.GRPCPort), logger)
	if err := httpGateway.Init(ctx); err != nil {
		return fmt.Errorf("init http gateway: %w", err)
	}

	swaggerHandler, err := swagger.NewHandler("api/gen/openapiv2/concord.swagger.json", "/docs", logger)
	if err != nil {
		logger.Warn("swagger handler not available", zap.Error(err))
	}

	httpMux := http.NewServeMux()
	httpMux.Handle("/files/", storageHandler)
	if swaggerHandler != nil {
		httpMux.Handle("/docs/", swaggerHandler)
		httpMux.Handle("/docs", http.RedirectHandler("/docs/", http.StatusMovedPermanently))
		logger.Info("swagger UI available at /docs")
	}
	httpMux.Handle("/", httpGateway)

	httpServer := &http.Server{
		Addr:    ":8080",
		Handler: httpMux,
	}

	go func() {
		logger.Info("HTTP server starting", zap.String("address", httpServer.Addr))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("http server: %w", err)
		}
	}()

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _ = typingSvc.CleanupExpired(cleanupCtx)
				cancel()
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

	done := make(chan struct{})
	go func() {
		cancel()

		logger.Info("stopping event hub")
		hubCtx, hubCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = eventsHub.Shutdown(hubCtx)
		hubCancel()

		logger.Info("stopping HTTP server")
		httpCtx, httpCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = httpServer.Shutdown(httpCtx)
		httpCancel()

		logger.Info("stopping gRPC server")
		grpcStopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(grpcStopped)
		}()

		select {
		case <-grpcStopped:
		case <-time.After(3 * time.Second):
			logger.Warn("forcing gRPC stop")
			grpcServer.Stop()
		}

		close(done)
	}()

	select {
	case <-done:
		logger.Info("shutdown complete")
	case <-time.After(8 * time.Second):
		logger.Warn("shutdown timeout, forcing exit")
		grpcServer.Stop()
	}

	return nil
}

func generateOpenAPISpec(logger *zap.Logger) error {
	specPath := "api/gen/openapiv2/concord.swagger.json"
	if _, err := os.Stat(specPath); err == nil {
		logger.Info("OpenAPI spec exists", zap.String("path", specPath))
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(specPath), 0755); err != nil {
		return err
	}

	logger.Info("generating OpenAPI spec...")
	cmd := exec.Command("buf", "generate", "--path", "api")
	cmd.Dir = "."
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buf generate failed: %w", err)
	}

	logger.Info("OpenAPI spec generated", zap.String("path", specPath))
	return nil
}
