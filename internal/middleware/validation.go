package middleware

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Validator is implemented by request messages that can self-validate. The
// ValidationInterceptor calls Validate on any request that satisfies it.
type Validator interface {
	Validate() error
}

// ValidationInterceptor returns a unary interceptor that runs Validate on any
// request implementing Validator, rejecting it with codes.InvalidArgument if
// validation fails. Requests that do not implement Validator pass through.
func ValidationInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if v, ok := req.(Validator); ok {
			if err := v.Validate(); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "validation failed: %v", err)
			}
		}

		return handler(ctx, req)
	}
}
