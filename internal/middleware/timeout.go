package middleware

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// longTimeoutMethods overrides the default request timeout for methods known to
// do heavier work (invites, message sends, room creation, friend requests),
// giving each the listed duration instead of the interceptor's default.
var longTimeoutMethods = map[string]time.Duration{
	"/concord.membership.v1.MembershipService/Invite":        10 * time.Second,
	"/concord.membership.v1.MembershipService/Remove":        10 * time.Second,
	"/concord.chat.v1.ChatService/SendMessage":               10 * time.Second,
	"/concord.rooms.v1.RoomsService/CreateRoom":              10 * time.Second,
	"/concord.friends.v1.FriendsService/SendFriendRequest":   10 * time.Second,
	"/concord.friends.v1.FriendsService/AcceptFriendRequest": 10 * time.Second,
}

// TimeoutInterceptor returns a unary interceptor that bounds each handler by a
// deadline: defaultTimeout, overridden by longTimeoutMethods for heavy methods
// and forced to 5s for any Health method. The handler runs in a goroutine; if
// the deadline fires first the interceptor returns codes.DeadlineExceeded while
// the handler goroutine keeps running (its context is cancelled).
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
