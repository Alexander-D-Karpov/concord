package interceptor

import (
	"context"
	"strings"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type contextKey string

const (
	userIDKey contextKey = "user_id"
	handleKey contextKey = "handle"
	claimsKey contextKey = "claims"
)

var publicMethods = map[string]bool{
	"/concord.auth.v1.AuthService/LoginPassword":                true,
	"/concord.auth.v1.AuthService/LoginOAuth":                   true,
	"/concord.auth.v1.AuthService/OAuthBegin":                   true,
	"/concord.auth.v1.AuthService/Refresh":                      true,
	"/concord.auth.v1.AuthService/Register":                     true,
	"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo": true,
	"/grpc.health.v1.Health/Check":                              true,
	"/grpc.health.v1.Health/Watch":                              true,

	"/concord.registry.v1.RegistryService/RegisterServer": true,
	"/concord.registry.v1.RegistryService/Heartbeat":      true,
	"/concord.registry.v1.RegistryService/ListServers":    true,
}

type AuthInterceptor struct {
	jwtManager *jwt.Manager
}

func NewAuthInterceptor(jwtManager *jwt.Manager) *AuthInterceptor {
	return &AuthInterceptor{
		jwtManager: jwtManager,
	}
}

func (a *AuthInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		logger := logging.FromContext(ctx)

		if publicMethods[info.FullMethod] {
			return handler(ctx, req)
		}

		newCtx, err := a.authenticate(ctx, logger)
		if err != nil {
			logger.Warn("authentication failed",
				zap.String("method", info.FullMethod),
				zap.Error(err),
			)
			return nil, errors.ToGRPCError(err)
		}

		return handler(newCtx, req)
	}
}

func (a *AuthInterceptor) Stream() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := ss.Context()
		logger := logging.FromContext(ctx)

		if publicMethods[info.FullMethod] {
			return handler(srv, ss)
		}

		newCtx, err := a.authenticate(ctx, logger)
		if err != nil {
			logger.Warn("authentication failed",
				zap.String("method", info.FullMethod),
				zap.Error(err),
			)
			return errors.ToGRPCError(err)
		}

		return handler(srv, &authenticatedStream{ServerStream: ss, ctx: newCtx})
	}
}

func (a *AuthInterceptor) authenticate(ctx context.Context, logger *zap.Logger) (context.Context, error) {
	if existingUserID := GetUserID(ctx); existingUserID != "" {
		return ctx, nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, errors.Unauthorized("missing metadata")
	}

	authHeaders := md.Get("authorization")
	if len(authHeaders) == 0 {
		return nil, errors.Unauthorized("missing authorization header")
	}

	authHeader := authHeaders[0]
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, errors.Unauthorized("invalid authorization header format")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")

	claims, err := a.jwtManager.ValidateAccessToken(token)
	if err != nil {
		return nil, errors.Unauthorized("invalid token")
	}

	ctx = context.WithValue(ctx, userIDKey, claims.UserID)
	ctx = context.WithValue(ctx, handleKey, claims.Handle)
	ctx = context.WithValue(ctx, claimsKey, claims)

	logger = logger.With(
		zap.String("user_id", claims.UserID),
		zap.String("handle", claims.Handle),
	)
	ctx = logging.WithLogger(ctx, logger)

	return ctx, nil
}

type authenticatedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authenticatedStream) Context() context.Context {
	return s.ctx
}

func GetUserID(ctx context.Context) string {
	if userID, ok := ctx.Value(userIDKey).(string); ok {
		return userID
	}
	return ""
}

func GetHandle(ctx context.Context) string {
	if handle, ok := ctx.Value(handleKey).(string); ok {
		return handle
	}
	return ""
}

func GetClaims(ctx context.Context) *jwt.Claims {
	if claims, ok := ctx.Value(claimsKey).(*jwt.Claims); ok {
		return claims
	}
	return nil
}

func ContextWithAuth(ctx context.Context, userID, handle string, claims *jwt.Claims) context.Context {
	ctx = context.WithValue(ctx, userIDKey, userID)
	ctx = context.WithValue(ctx, handleKey, handle)
	if claims != nil {
		ctx = context.WithValue(ctx, claimsKey, claims)
	}
	return ctx
}
