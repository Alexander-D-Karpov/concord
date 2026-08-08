package observability

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// contextKey is a private type for context keys defined in this package, so they
// cannot collide with keys from other packages.
type contextKey string

const (
	// requestIDKey stores the per-request ID in the context.
	requestIDKey contextKey = "request_id"
	// correlationIDKey stores the cross-request correlation ID in the context.
	correlationIDKey contextKey = "correlation_id"
	// loggerKey stores the request-scoped logger enriched with the IDs and method.
	loggerKey contextKey = "logger"
)

// RequestIDInterceptor returns a unary interceptor that ensures each request has
// a request ID and correlation ID (reused from incoming metadata or generated),
// stores them in the context, and attaches a logger enriched with those IDs and
// the method for downstream handlers.
func RequestIDInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		requestID := extractRequestID(ctx)
		correlationID := extractCorrelationID(ctx)

		ctx = context.WithValue(ctx, requestIDKey, requestID)
		ctx = context.WithValue(ctx, correlationIDKey, correlationID)

		enrichedLogger := logger.With(
			zap.String("request_id", requestID),
			zap.String("correlation_id", correlationID),
			zap.String("method", info.FullMethod),
		)

		ctx = context.WithValue(ctx, loggerKey, enrichedLogger)

		return handler(ctx, req)
	}
}

// extractRequestID returns the x-request-id from incoming metadata, or a freshly
// generated ID when the header is absent.
func extractRequestID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return generateID()
	}

	if vals := md.Get("x-request-id"); len(vals) > 0 {
		return vals[0]
	}

	return generateID()
}

// extractCorrelationID returns the x-correlation-id from incoming metadata, or a
// freshly generated ID when the header is absent.
func extractCorrelationID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return generateID()
	}

	if vals := md.Get("x-correlation-id"); len(vals) > 0 {
		return vals[0]
	}

	return generateID()
}

// generateID returns a new random UUID string used as a request or correlation
// ID.
func generateID() string {
	return uuid.New().String()
}

// GetRequestID returns the request ID stored in ctx by RequestIDInterceptor, or
// "" if none is present.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// GetCorrelationID returns the correlation ID stored in ctx by
// RequestIDInterceptor, or "" if none is present.
func GetCorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey).(string); ok {
		return id
	}
	return ""
}
