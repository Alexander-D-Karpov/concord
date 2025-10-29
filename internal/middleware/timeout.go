package middleware

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TimeoutInterceptor(timeout time.Duration) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		type result struct {
			resp interface{}
			err  error
		}

		resultChan := make(chan result, 1)

		go func() {
			resp, err := handler(ctx, req)
			resultChan <- result{resp: resp, err: err}
		}()

		select {
		case res := <-resultChan:
			return res.resp, res.err
		case <-ctx.Done():
			return nil, status.Errorf(codes.DeadlineExceeded, "request timeout exceeded")
		}
	}
}
