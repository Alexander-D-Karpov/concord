package ratelimit

import (
	"context"
	"strings"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded, please try again later")
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
			return status.Error(codes.ResourceExhausted, "rate limit exceeded, please try again later")
		}

		return handler(srv, ss)
	}
}

func (i *Interceptor) getKey(ctx context.Context, method string) string {
	userID := interceptor.GetUserID(ctx)
	if userID != "" {
		if strings.Contains(method, "Auth") {
			return "auth"
		}
		if strings.Contains(method, "Message") || strings.Contains(method, "Chat") {
			return "message"
		}
		if strings.Contains(method, "Upload") {
			return "upload"
		}
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
