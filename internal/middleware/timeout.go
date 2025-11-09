package middleware

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var longTimeoutMethods = map[string]time.Duration{
	"/concord.membership.v1.MembershipService/Invite":        10 * time.Second,
	"/concord.membership.v1.MembershipService/Remove":        10 * time.Second,
	"/concord.chat.v1.ChatService/SendMessage":               10 * time.Second,
	"/concord.rooms.v1.RoomsService/CreateRoom":              10 * time.Second,
	"/concord.friends.v1.FriendsService/SendFriendRequest":   10 * time.Second,
	"/concord.friends.v1.FriendsService/AcceptFriendRequest": 10 * time.Second,
}

func TimeoutInterceptor(defaultTimeout time.Duration) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		timeout := defaultTimeout

		// Check if this method needs a longer timeout
		if customTimeout, exists := longTimeoutMethods[info.FullMethod]; exists {
			timeout = customTimeout
		}

		// Skip timeout for health checks
		if strings.Contains(info.FullMethod, "Health") {
			timeout = 5 * time.Second
		}

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
