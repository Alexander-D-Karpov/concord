package ratelimit

import (
	"context"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type Interceptor struct {
	limiter *Limiter
}

func NewInterceptor(limiter *Limiter) *Interceptor {
	return &Interceptor{limiter: limiter}
}

func (i *Interceptor) Unary() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		key := i.getKey(ctx, info.FullMethod)

		allowed, err := i.limiter.Allow(ctx, key)
		if err != nil {
			return nil, err
		}

		if !allowed {
			return nil, errors.ToGRPCError(errors.NewAppError(
				429,
				"rate limit exceeded",
				nil,
			))
		}

		return handler(ctx, req)
	}
}

func (i *Interceptor) Stream() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := ss.Context()
		key := i.getKey(ctx, info.FullMethod)

		allowed, err := i.limiter.Allow(ctx, key)
		if err != nil {
			return err
		}

		if !allowed {
			return errors.ToGRPCError(errors.NewAppError(
				429,
				"rate limit exceeded",
				nil,
			))
		}

		return handler(srv, ss)
	}
}

func (i *Interceptor) getKey(ctx context.Context, method string) string {
	userID := interceptor.GetUserID(ctx)
	if userID != "" {
		return "user:" + userID
	}

	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if xff := md.Get("x-forwarded-for"); len(xff) > 0 {
			return "ip:" + xff[0]
		}
		if realIP := md.Get("x-real-ip"); len(realIP) > 0 {
			return "ip:" + realIP[0]
		}
	}

	return "method:" + method
}
