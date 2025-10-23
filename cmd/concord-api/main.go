package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	chatv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/chat/v1"
	membershipv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/membership/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	usersv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/users/v1"
	authsvc "github.com/Alexander-D-Karpov/concord/internal/auth"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/auth/oauth"
	"github.com/Alexander-D-Karpov/concord/internal/call"
	"github.com/Alexander-D-Karpov/concord/internal/chat"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/infra/migrations"
	"github.com/Alexander-D-Karpov/concord/internal/membership"
	"github.com/Alexander-D-Karpov/concord/internal/messages"
	"github.com/Alexander-D-Karpov/concord/internal/ratelimit"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/stream"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
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
	defer func() {
		_ = logger.Sync()
	}()

	logger.Info("starting concord-api",
		zap.String("version", "0.1.0"),
		zap.Int("grpc_port", cfg.Server.GRPCPort),
	)

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
			defer func(cacheClient *cache.Cache) {
				err := cacheClient.Close()
				if err != nil {
					logger.Error("failed to close Redis client", zap.Error(err))
				}
			}(cacheClient)
			logger.Info("connected to Redis")
		}
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
		rateLimiter = ratelimit.NewLimiter(nil, 0, 0, false)
	}
	rateLimitInterceptor := ratelimit.NewInterceptor(rateLimiter)

	snowflakeGen := infra.NewSnowflakeGenerator(1)

	usersRepo := users.NewRepository(database.Pool)
	usersService := users.NewService(usersRepo)
	usersHandler := users.NewHandler(usersService)

	roomsRepo := rooms.NewRepository(database.Pool)
	roomsService := rooms.NewService(roomsRepo)
	roomsHandler := rooms.NewHandler(roomsService)

	messagesRepo := messages.NewRepository(database.Pool, snowflakeGen)
	eventsHub := events.NewHub(logger)

	chatService := chat.NewService(messagesRepo, eventsHub)
	chatHandler := chat.NewHandler(chatService)

	membershipService := membership.NewService(roomsRepo)
	membershipHandler := membership.NewHandler(membershipService)

	streamHandler := stream.NewHandler(eventsHub)

	voiceAssignService := voiceassign.NewService(database.Pool, jwtManager)
	callHandler := call.NewHandler(voiceAssignService, roomsRepo)

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

	interceptors := []grpc.UnaryServerInterceptor{
		loggingInterceptor(logger),
		rateLimitInterceptor.Unary(),
		authInterceptor.Unary(),
	}

	streamInterceptors := []grpc.StreamServerInterceptor{
		rateLimitInterceptor.Stream(),
		authInterceptor.Stream(),
	}

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(interceptors...),
		grpc.ChainStreamInterceptor(streamInterceptors...),
	)

	authv1.RegisterAuthServiceServer(grpcServer, authHandler)
	usersv1.RegisterUsersServiceServer(grpcServer, usersHandler)
	roomsv1.RegisterRoomsServiceServer(grpcServer, roomsHandler)
	chatv1.RegisterChatServiceServer(grpcServer, chatHandler)
	membershipv1.RegisterMembershipServiceServer(grpcServer, membershipHandler)
	streamv1.RegisterStreamServiceServer(grpcServer, streamHandler)
	callv1.RegisterCallServiceServer(grpcServer, callHandler)

	reflection.Register(grpcServer)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Server.GRPCPort))
	if err != nil {
		return fmt.Errorf("create listener: %w", err)
	}

	logger.Info("gRPC server listening", zap.String("address", listener.Addr().String()))

	errChan := make(chan error, 1)
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			errChan <- fmt.Errorf("serve grpc: %w", err)
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
	grpcServer.GracefulStop()

	logger.Info("shutdown complete")
	return nil
}

func loggingInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		ctx = logging.WithLogger(ctx, logger)

		resp, err := handler(ctx, req)

		if err != nil {
			st, ok := status.FromError(err)
			if !ok {
				logger.Error("request failed with non-status error",
					zap.String("method", info.FullMethod),
					zap.Error(err),
				)
				return resp, err
			}

			code := st.Code()

			switch code {
			case codes.Unauthenticated, codes.PermissionDenied:
				logger.Debug("authentication/authorization failed",
					zap.String("method", info.FullMethod),
					zap.String("code", code.String()),
					zap.String("message", st.Message()),
				)
			case codes.InvalidArgument, codes.NotFound, codes.AlreadyExists:
				logger.Info("client error",
					zap.String("method", info.FullMethod),
					zap.String("code", code.String()),
					zap.String("message", st.Message()),
				)
			case codes.Internal, codes.Unknown, codes.DataLoss:
				logger.Error("internal server error",
					zap.String("method", info.FullMethod),
					zap.String("code", code.String()),
					zap.String("message", st.Message()),
					zap.Error(err),
				)
			default:
				logger.Warn("request failed",
					zap.String("method", info.FullMethod),
					zap.String("code", code.String()),
					zap.String("message", st.Message()),
				)
			}
		}

		return resp, err
	}
}
