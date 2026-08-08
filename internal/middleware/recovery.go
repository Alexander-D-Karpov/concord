package middleware

import (
	"context"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// quietMethodPrefixes lists gRPC method prefixes whose successful and cancelled
// requests are logged at trace level instead of debug/info, to keep noisy
// internal traffic (registry, health, reflection) out of normal logs.
var quietMethodPrefixes = []string{
	"/concord.registry.v1.",
	"/grpc.health.v1.",
	"/grpc.reflection.",
}

// isQuietMethod reports whether method matches any quietMethodPrefixes entry
// and should therefore be logged at reduced verbosity.
func isQuietMethod(method string) bool {
	for _, prefix := range quietMethodPrefixes {
		if strings.HasPrefix(method, prefix) {
			return true
		}
	}
	return false
}

// RecoveryInterceptor returns a unary interceptor that recovers from panics in
// the handler, logs the panic with its stack trace, and converts it into a
// codes.Internal error so a single bad request cannot crash the server.
func RecoveryInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic recovered",
					zap.Any("panic", r),
					zap.String("method", info.FullMethod),
					zap.String("stack", string(debug.Stack())),
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()

		return handler(ctx, req)
	}
}

// StreamRecoveryInterceptor is the streaming counterpart of RecoveryInterceptor:
// it recovers panics raised while serving a stream and returns codes.Internal.
func StreamRecoveryInterceptor(logger *zap.Logger) grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic recovered in stream",
					zap.Any("panic", r),
					zap.String("method", info.FullMethod),
					zap.String("stack", string(debug.Stack())),
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()

		return handler(srv, ss)
	}
}

// RequestLogInterceptor returns a unary interceptor that logs each completed
// request via logRequest with its method, resulting status code, and latency.
func RequestLogInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logRequest(ctx, info.FullMethod, err, time.Since(start))
		return resp, err
	}
}

// StreamRequestLogInterceptor is the streaming counterpart of
// RequestLogInterceptor, logging the stream's method, status, and duration once
// the handler returns.
func StreamRequestLogInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		start := time.Now()
		err := handler(srv, ss)
		logRequest(ss.Context(), info.FullMethod, err, time.Since(start))
		return err
	}
}

// logRequest emits a single structured log line for a finished request. The log
// level is chosen from the status code: quiet methods are downgraded to trace
// (or warn on error), expected client-facing codes log at debug, resource
// exhaustion and deadlines at warn, and anything else at error.
func logRequest(ctx context.Context, method string, err error, duration time.Duration) {
	logger := logging.FromContext(ctx)
	code := status.Code(err)

	fields := []zap.Field{
		zap.String("method", method),
		zap.Duration("duration", duration),
	}
	if err != nil {
		fields = append(fields,
			zap.String("code", code.String()),
			zap.Error(err),
		)
	}

	if isQuietMethod(method) {
		if err == nil || code == codes.Canceled {
			logging.Trace(logger, "handled request", fields...)
			return
		}
		logger.Warn("handled request", fields...)
		return
	}

	switch code {
	case codes.OK,
		codes.Canceled,
		codes.InvalidArgument,
		codes.NotFound,
		codes.AlreadyExists,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.FailedPrecondition:
		logger.Debug("handled request", fields...)
	case codes.ResourceExhausted, codes.DeadlineExceeded:
		logger.Warn("handled request", fields...)
	default:
		logger.Error("handled request", fields...)
	}
}
