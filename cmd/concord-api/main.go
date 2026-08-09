package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	featuresv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/features/v1"
	unfurlv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/unfurl/v1"
	"github.com/Alexander-D-Karpov/concord/internal/features"
	"github.com/Alexander-D-Karpov/concord/internal/features/gifprovider"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/editing"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/linkpreview"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/mentions"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/polls"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/readtracking"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/slowmode"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/typing"
	"github.com/Alexander-D-Karpov/concord/internal/security"
	"github.com/Alexander-D-Karpov/concord/internal/swagger"
	"github.com/Alexander-D-Karpov/concord/internal/version"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	adminv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/admin/v1"
	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	chatv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/chat/v1"
	dmv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/dm/v1"
	friendsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/friends/v1"
	membershipv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/membership/v1"
	pushv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/push/v1"
	registryv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/registry/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	usersv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/users/v1"
	"github.com/Alexander-D-Karpov/concord/internal/admin"
	"github.com/Alexander-D-Karpov/concord/internal/audit"
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
	"github.com/Alexander-D-Karpov/concord/internal/gateway"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/infra/migrations"
	"github.com/Alexander-D-Karpov/concord/internal/membership"
	"github.com/Alexander-D-Karpov/concord/internal/middleware"
	"github.com/Alexander-D-Karpov/concord/internal/observability"
	"github.com/Alexander-D-Karpov/concord/internal/push"
	"github.com/Alexander-D-Karpov/concord/internal/ratelimit"
	"github.com/Alexander-D-Karpov/concord/internal/registry"
	"github.com/Alexander-D-Karpov/concord/internal/retention"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/social/friends"
	"github.com/Alexander-D-Karpov/concord/internal/storage"
	"github.com/Alexander-D-Karpov/concord/internal/stream"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
)

// main runs the API server, printing any fatal error to stderr and exiting non-zero.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// run is the composition root for concord-api. It loads config (and .env), the
// -debug flag forces VOICE_DEBUG on, then wires up logging, the Postgres pool (with
// migrations), optional Redis cache, all domain services and their gRPC handlers,
// the interceptor chains, the gRPC server, and an HTTP mux fronting the gRPC-gateway,
// file storage, swagger docs and /version. It launches the gRPC, metrics (9100),
// health (8081) and HTTP (8080) servers plus background workers, then blocks until a
// signal or a server error and performs a bounded graceful shutdown of the hub, HTTP
// and gRPC servers.
func run() error {
	_ = godotenv.Load(".env")

	debug := flag.Bool("debug", false, "enable voice debug mode (fast-join + rate-limit bypass); "+
		"overrides VOICE_DEBUG. Lets the throughput harness join without invites — NEVER use in production")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if *debug {
		cfg.Voice.Debug = true
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
		zap.String("version", version.API()),
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
	healthChecker := observability.NewHealthChecker(logger, version.API())

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

	// The rate-limit bypass token is honored only when VOICE_DEBUG is on, so the
	// stress harness can bypass limits against a debug deployment but production
	// (VOICE_DEBUG off) ignores the token entirely and cannot be flooded through it.
	// This keeps "easy stress test only with voice debug" true for rate limiting too.
	bypassToken := ""
	if cfg.Voice.Debug {
		bypassToken = cfg.RateLimit.BypassToken
	}

	var rateLimiter *ratelimit.Limiter
	if cfg.RateLimit.Enabled {
		rateLimiter = ratelimit.NewLimiter(
			cacheClient,
			cfg.RateLimit.RequestsPerMinute,
			cfg.RateLimit.Burst,
			true,
			bypassToken,
		)
		logger.Info("rate limiting enabled")
	} else {
		rateLimiter = ratelimit.NewLimiter(nil, 500, 100, false, bypassToken)
	}
	rateLimitInterceptor := ratelimit.NewInterceptor(rateLimiter)

	storageService, err := storage.New(cfg.Storage.Path, cfg.Storage.URL, logger)
	if err != nil {
		return fmt.Errorf("init storage: %w", err)
	}
	storageHandler := storage.NewHandler(storageService, logger)

	snowflakeGen := infra.NewSnowflakeGenerator(1)

	editRecorder := editing.NewRecorder()
	editReader := editing.NewReader(database.Pool)
	slowmodeSvc := slowmode.NewService(database.Pool, aside)
	mentionParser := mentions.NewParser(database.Pool)

	usersRepo := users.NewRepository(database.Pool)
	if cacheClient != nil {
		usersRepo = users.NewRepositoryWithCache(database.Pool, cacheClient)
	}

	eventsHub := events.NewHub(logger, database.Pool, aside)

	presenceManager := users.NewPresenceManager(usersRepo, eventsHub)
	defer presenceManager.Stop()

	usersService := users.NewService(usersRepo, eventsHub, presenceManager, cfg.Storage.Path, cfg.Storage.URL)
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
	chatService := chat.NewService(messagesRepo, roomsRepo, eventsHub, aside, slowmodeSvc, mentionParser, editRecorder, editReader)
	chatHandler := chat.NewHandler(chatService, storageService, readTrackingSvc, typingSvc)

	membershipService := membership.NewService(roomsRepo, eventsHub, aside)
	membershipHandler := membership.NewHandler(membershipService)

	streamHandler := stream.NewHandler(eventsHub, presenceManager)

	voiceAssignService := voiceassign.NewService(database.Pool, jwtManager, cacheClient, roomsRepo, eventsHub)
	if cfg.Voice.SinglePort {
		// single public UDP port: the room→port hash collapses to that one port
		voiceAssignService.SetPortCount(1)
	}
	voiceAssignService.SetTCPPort(cfg.Voice.TCPPort)
	membershipService.SetKeyRotator(voiceAssignService)
	if cfg.Voice.Debug {
		logger.Warn("VOICE_DEBUG is ENABLED: voice-join RPCs skip the room-membership check and " +
			"the rate-limit bypass token is honored (fast-join + easy stress for the throughput " +
			"harness). NEVER enable this in production.")
	}
	callHandler := call.NewHandler(voiceAssignService, roomsRepo, eventsHub, logger, cfg.Voice.Debug)
	streamHandler.SetVoiceSnapshotSender(
		call.NewSnapshotter(voiceAssignService, database.Pool, eventsHub, logger),
	)

	go voiceAssignService.StartHealthChecker(ctx, 30*time.Second)

	featuresRepo := features.NewRepository(database.Pool)
	gifProvider := gifprovider.NewTenorProvider(os.Getenv("GIF_API_KEY"))
	featuresService := features.NewService(featuresRepo, eventsHub, snowflakeGen, logger, gifProvider)
	go featuresService.RunScheduler(ctx)

	pollsRepo := polls.NewRepository(database.Pool)
	pollsService := polls.NewService(pollsRepo, featuresRepo, eventsHub, snowflakeGen, aside, database.Pool, logger)
	go pollsService.RunCloser(ctx)

	retentionService := retention.NewService(database.Pool, logger)
	go retentionService.RunPurger(ctx, time.Hour)

	featuresAggregator := features.NewAggregator(featuresService, pollsService, slowmodeSvc)

	friendsRepo := friends.NewRepository(database.Pool)
	if cacheClient != nil {
		friendsRepo = friends.NewRepositoryWithCache(database.Pool, cacheClient)
	}
	friendsService := friends.NewService(friendsRepo, eventsHub, usersRepo, presenceManager)
	friendsHandler := friends.NewHandler(friendsService)
	friendsService.SetKeyRotator(voiceAssignService)

	auditLogger := audit.NewLogger(database.Pool, logger)
	adminService := admin.NewService(database.Pool, roomsRepo, eventsHub, logger, auditLogger)
	adminService.SetVoiceEvictor(voiceAssignService)
	adminHandler := admin.NewHandler(adminService)

	pushRepo := push.NewRepository(database.Pool)
	pushHandler := push.NewHandler(pushRepo)

	dmRepo := dm.NewRepository(database.Pool)
	dmMsgRepo := dm.NewMessageRepository(database.Pool, snowflakeGen, editRecorder)
	dmService := dm.NewService(dmRepo, dmMsgRepo, usersRepo, eventsHub, voiceAssignService, presenceManager, mentionParser, editReader, aside, logger)
	dmHandler := dm.NewHandler(dmService, storageService)
	dmHandler.SetReadTrackingService(readTrackingSvc)
	dmHandler.SetTypingService(typingSvc)

	if cfg.Push.Enabled {
		// TODO(push): when the FCM adapter lands, use push.NewFCMSender(ctx, cfg.Push.CredentialsFile)
		// if cfg.Push.CredentialsFile != ""; until then deliveries are dropped by the noop sender.
		sender := push.NewNoopSender()
		pushDispatcher := push.NewDispatcher(pushRepo, sender, logger)
		pushDispatcher.Start(ctx)
		pushNotifier := push.NewNotifier(push.NewMuteChecker(featuresRepo), pushDispatcher, logger)
		chatService.SetPusher(pushNotifier)
		dmService.SetPusher(pushNotifier)
		logger.Info("push notifications enabled")
	}

	unfurlService := unfurl.NewService(cacheClient)
	unfurlHandler := unfurl.NewHandler(unfurlService)

	var oauthManager *oauth.Manager
	if len(cfg.Auth.OAuth) > 0 {
		oauthManager = oauth.NewManager(cfg.Auth.OAuth, nil, logger)
		// Validate provider availability once synchronously so the first
		// ListAuthMethods response is accurate, then keep it fresh in the
		// background so a provider that was briefly unreachable at boot recovers.
		vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		oauthManager.RefreshAvailability(vctx)
		cancel()
		go oauthManager.StartValidation(ctx, 5*time.Minute)
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
	// Ingest OAuth profile pictures into local avatar storage on first login.
	authService.SetAvatarIngestion(cfg.Storage.Path, cfg.Storage.URL)
	authHandler := authsvc.NewHandler(authService)

	registryService := registry.NewService(database.Pool, logger)
	registryHandler := registry.NewHandler(registryService)

	machineAuthInterceptor := registry.NewMachineAuthInterceptor(
		registryService,
		cfg.Voice.RegisterSecret,
	)

	interceptors := []grpc.UnaryServerInterceptor{
		middleware.RecoveryInterceptor(logger),
		observability.RequestIDInterceptor(logger),
		metrics.UnaryServerInterceptor(),
		middleware.TimeoutInterceptor(60 * time.Second),
		machineAuthInterceptor.Unary(),
		authInterceptor.Unary(),
		middleware.RequestLogInterceptor(),
		rateLimitInterceptor.Unary(),
	}

	streamInterceptors := []grpc.StreamServerInterceptor{
		middleware.StreamRecoveryInterceptor(logger),
		metrics.StreamServerInterceptor(),
		authInterceptor.Stream(),
		middleware.StreamRequestLogInterceptor(),
		rateLimitInterceptor.Stream(),
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
		logger.Info("gRPC server TLS enabled", zap.String("cert", cfg.Server.TLSCertFile))
	}

	serverOpts = append(serverOpts, keepaliveServerOptions()...)

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
	unfurlv1.RegisterUnfurlServiceServer(grpcServer, unfurlHandler)
	featuresv1.RegisterFeaturesServiceServer(grpcServer, featuresAggregator)
	pushv1.RegisterPushServiceServer(grpcServer, pushHandler)
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

	gatewayDialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if cfg.Server.TLSCertFile != "" && cfg.Server.TLSKeyFile != "" {
		certPEM, err := os.ReadFile(cfg.Server.TLSCertFile)
		if err != nil {
			return fmt.Errorf("read gateway TLS cert: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(certPEM) {
			return fmt.Errorf("read gateway TLS cert: failed to append server certificate")
		}
		gatewayDialOpts = []grpc.DialOption{
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
				MinVersion: tls.VersionTLS13,
				ServerName: "localhost",
				RootCAs:    roots,
			})),
		}
	}

	httpGateway := gateway.New(fmt.Sprintf("localhost:%d", cfg.Server.GRPCPort), logger, gatewayDialOpts...)
	if err := httpGateway.Init(ctx); err != nil {
		return fmt.Errorf("init http gateway: %w", err)
	}

	swaggerHandler, err := swagger.NewHandler("api/gen/openapiv2/concord.swagger.json", "/docs", logger)
	if err != nil {
		logger.Warn("swagger handler not available", zap.Error(err))
	}

	httpMux := http.NewServeMux()
	httpMux.Handle("/files/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) > 7 && r.URL.Path[7:14] == "avatars" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		storageHandler.ServeHTTP(w, r)
	}))
	if swaggerHandler != nil {
		httpMux.Handle("/docs/", swaggerHandler)
		httpMux.Handle("/docs", http.RedirectHandler("/docs/", http.StatusMovedPermanently))
		logger.Info("swagger UI available at /docs")
	}
	// The root path serves the API docs (Swagger UI); every other path falls
	// through to the REST gateway. This is the catch-all handler, so it must
	// delegate non-root requests to the gateway to keep /v1/... working. When
	// swagger is unavailable, "/" is handled by the gateway as before.
	httpMux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && swaggerHandler != nil {
			http.Redirect(w, r, "/docs/", http.StatusFound)
			return
		}
		httpGateway.ServeHTTP(w, r)
	}))

	httpMux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"api":       version.API(),
			"codename":  version.APICodename(),
			"api_major": version.APIMajor,
			"api_minor": version.APIMinor,
		}
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	})

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
				presenceManager.ReapOfflineGrace(cleanupCtx, 60*time.Second)
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

// keepaliveServerOptions configures HTTP/2 keepalive so idle mobile streams stay
// alive and dead connections are detected. ServerParameters pings idle conns; the
// EnforcementPolicy permits client pings even without an active stream so a
// backgrounded phone holding an idle stream isn't disconnected as abusive.
func keepaliveServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second, // ping an idle connection after 30s of inactivity
			Timeout: 20 * time.Second, // wait 20s for the ping ack before closing
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // clients may ping at most every 10s
			PermitWithoutStream: true,             // allow pings on idle streams
		}),
	}
}

// generateOpenAPISpec verifies the checked-in swagger spec is present, logging its
// location if so. It does not actually generate anything: when the file is missing it
// warns to run 'make proto' and returns an error (which the caller treats as non-fatal).
func generateOpenAPISpec(logger *zap.Logger) error {
	specPath := "api/gen/openapiv2/concord.swagger.json"
	if _, err := os.Stat(specPath); err == nil {
		logger.Info("OpenAPI spec exists", zap.String("path", specPath))
		return nil
	}
	logger.Warn("OpenAPI spec not found — run 'make proto' to generate", zap.String("path", specPath))
	return fmt.Errorf("spec not found at %s", specPath)
}
