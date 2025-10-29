package observability

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type contextKey string

const (
	requestIDKey     contextKey = "request_id"
	correlationIDKey contextKey = "correlation_id"
	loggerKey        contextKey = "logger"
)

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

func generateID() string {
	return uuid.New().String()
}

func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

func GetCorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey).(string); ok {
		return id
	}
	return ""
}
