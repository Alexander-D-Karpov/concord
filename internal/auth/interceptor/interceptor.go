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

// contextKey is a private type for this package's context keys, avoiding
// collisions with keys defined in other packages.
type contextKey string

const (
	// userIDKey is the context key under which the authenticated user ID is stored.
	userIDKey contextKey = "user_id"
	// handleKey is the context key under which the authenticated user handle is stored.
	handleKey contextKey = "handle"
	// claimsKey is the context key under which the full *jwt.Claims is stored.
	claimsKey contextKey = "claims"
)

// publicMethods lists gRPC full-method names that skip authentication entirely
// (login/register/refresh, reflection, health). A method absent from this map is
// authenticated; forgetting to add a genuinely public RPC causes 401s.
var publicMethods = map[string]bool{
	"/concord.auth.v1.AuthService/LoginPassword":                true,
	"/concord.auth.v1.AuthService/LoginOAuth":                   true,
	"/concord.auth.v1.AuthService/OAuthBegin":                   true,
	"/concord.auth.v1.AuthService/Refresh":                      true,
	"/concord.auth.v1.AuthService/Register":                     true,
	"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo": true,
	"/grpc.health.v1.Health/Check":                              true,
	"/grpc.health.v1.Health/Watch":                              true,
}

// machineAuthMethods lists RPCs that use machine-to-machine auth instead of user
// JWTs; this interceptor skips them so the dedicated machine-auth interceptor
// (registry.MachineAuthInterceptor) can handle them.
var machineAuthMethods = map[string]bool{
	"/concord.registry.v1.RegistryService/RegisterServer": true,
	"/concord.registry.v1.RegistryService/Heartbeat":      true,
}

// AuthInterceptor authenticates gRPC calls by validating a Bearer access token and
// injecting the caller's identity into the request context.
type AuthInterceptor struct {
	jwtManager *jwt.Manager
}

// NewAuthInterceptor returns an AuthInterceptor that validates tokens with jwtManager.
func NewAuthInterceptor(jwtManager *jwt.Manager) *AuthInterceptor {
	return &AuthInterceptor{
		jwtManager: jwtManager,
	}
}

// Unary returns a unary server interceptor that bypasses public and machine-auth
// methods and otherwise authenticates the caller, rejecting the call with a gRPC
// error if authentication fails.
func (a *AuthInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		logger := logging.FromContext(ctx)

		if publicMethods[info.FullMethod] || machineAuthMethods[info.FullMethod] {
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

// Stream returns a stream server interceptor that authenticates the caller and
// wraps the stream so downstream handlers see the identity-enriched context. Unlike
// Unary it only skips publicMethods (machine-auth RPCs are unary), and it rejects
// unauthenticated streams with a gRPC error.
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

// authenticate extracts and validates the Bearer access token from the incoming
// metadata, returning a context carrying the user ID, handle, claims, and an
// identity-tagged logger. If the context already carries a user ID (e.g. set by an
// upstream interceptor) it is trusted and returned unchanged. Returns Unauthorized
// on missing metadata, a missing/malformed header, or an invalid token.
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

// authenticatedStream wraps a grpc.ServerStream to override its context with one
// carrying the authenticated identity.
type authenticatedStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the identity-enriched context instead of the underlying stream's.
func (s *authenticatedStream) Context() context.Context {
	return s.ctx
}

// GetUserID returns the authenticated user ID from ctx, or "" if the request is
// unauthenticated.
func GetUserID(ctx context.Context) string {
	if userID, ok := ctx.Value(userIDKey).(string); ok {
		return userID
	}
	return ""
}

// GetHandle returns the authenticated user handle from ctx, or "" if absent.
func GetHandle(ctx context.Context) string {
	if handle, ok := ctx.Value(handleKey).(string); ok {
		return handle
	}
	return ""
}

// GetClaims returns the full validated *jwt.Claims from ctx, or nil if the request
// is unauthenticated.
func GetClaims(ctx context.Context) *jwt.Claims {
	if claims, ok := ctx.Value(claimsKey).(*jwt.Claims); ok {
		return claims
	}
	return nil
}

// ContextWithAuth returns a copy of ctx populated with the given identity, letting
// non-gRPC callers (e.g. tests or internal dispatch) supply an authenticated
// context that GetUserID/GetHandle/GetClaims can read. claims may be nil.
func ContextWithAuth(ctx context.Context, userID, handle string, claims *jwt.Claims) context.Context {
	ctx = context.WithValue(ctx, userIDKey, userID)
	ctx = context.WithValue(ctx, handleKey, handle)
	if claims != nil {
		ctx = context.WithValue(ctx, claimsKey, claims)
	}
	return ctx
}
