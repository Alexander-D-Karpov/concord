package ratelimit

import (
	"context"
	"net"
	"strings"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// BypassMetadataKey is the gRPC metadata key carrying the rate-limit bypass
// token. A matching token skips rate limiting entirely and is only honored in
// voice-debug builds (see Limiter.ShouldBypass).
const BypassMetadataKey = "x-concord-ratelimit-bypass"

// methodCategories maps specific full gRPC method names to the rate-limit
// Category used for them. Methods not listed here are categorized heuristically
// by classify.
var methodCategories = map[string]Category{
	"/concord.auth.v1.AuthService/Register":      CategoryAuth,
	"/concord.auth.v1.AuthService/LoginPassword": CategoryAuth,
	"/concord.auth.v1.AuthService/LoginOAuth":    CategoryAuth,
	"/concord.auth.v1.AuthService/OAuthBegin":    CategoryAuth,
	"/concord.auth.v1.AuthService/Refresh":       CategoryAuth,
	"/concord.auth.v1.AuthService/Logout":        CategoryAuth,

	"/concord.chat.v1.ChatService/SendMessage": CategoryMessage,
	"/concord.chat.v1.ChatService/EditMessage": CategoryMessage,
	"/concord.dm.v1.DMService/SendDM":          CategoryMessage,
	"/concord.dm.v1.DMService/EditDM":          CategoryMessage,

	"/concord.features.v1.FeaturesService/ForwardMessages": CategoryMessage,
	"/concord.features.v1.FeaturesService/ScheduleMessage": CategoryMessage,
	"/concord.features.v1.FeaturesService/CreatePoll":      CategoryMessage,

	"/concord.users.v1.UsersService/UploadAvatar": CategoryUpload,

	"/concord.chat.v1.ChatService/StartTyping":        CategoryEphemeral,
	"/concord.chat.v1.ChatService/StopTyping":         CategoryEphemeral,
	"/concord.chat.v1.ChatService/MarkAsRead":         CategoryEphemeral,
	"/concord.dm.v1.DMService/StartDMTyping":          CategoryEphemeral,
	"/concord.dm.v1.DMService/StopDMTyping":           CategoryEphemeral,
	"/concord.dm.v1.DMService/MarkDMAsRead":           CategoryEphemeral,
	"/concord.features.v1.FeaturesService/SaveDraft":  CategoryEphemeral,
	"/concord.features.v1.FeaturesService/ClearDraft": CategoryEphemeral,
	"/concord.call.v1.CallService/GetVoiceStatus":     CategoryEphemeral,
	"/concord.call.v1.CallService/SetMediaPrefs":      CategoryEphemeral,
}

// exemptPrefixes lists method prefixes (registry, reflection, health) that are
// never rate limited and are classified as CategoryExempt.
var exemptPrefixes = []string{
	"/concord.registry.v1.",
	"/grpc.reflection.",
	"/grpc.health.",
}

// Interceptor adapts a Limiter into gRPC unary and stream interceptors.
type Interceptor struct {
	limiter *Limiter
}

// NewInterceptor returns an Interceptor that enforces limits using the given
// Limiter.
func NewInterceptor(limiter *Limiter) *Interceptor {
	return &Interceptor{limiter: limiter}
}

// Unary returns a unary interceptor that runs the rate-limit check before the
// handler, rejecting over-limit requests with codes.ResourceExhausted.
func (i *Interceptor) Unary() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if err := i.check(ctx, info.FullMethod); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// Stream returns a stream interceptor that applies the rate-limit check once at
// stream establishment before delegating to the handler.
func (i *Interceptor) Stream() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if err := i.check(ss.Context(), info.FullMethod); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

// check enforces the rate limit for method. It returns nil (allowing the call)
// when the bypass token is present, the method is exempt, or the limiter itself
// errors (fail-open). It returns codes.ResourceExhausted only when the limiter
// positively reports the caller is over its limit.
func (i *Interceptor) check(ctx context.Context, method string) error {
	if i.limiter.ShouldBypass(ctx) {
		return nil
	}

	cat := classify(method)
	if cat == CategoryExempt {
		return nil
	}

	id := identity(ctx)

	allowed, err := i.limiter.Allow(ctx, cat, id)
	if err != nil {
		logging.FromContext(ctx).Warn("rate limit check failed, allowing request",
			zap.String("method", method),
			zap.Error(err),
		)
		return nil
	}

	if !allowed {
		cfg := i.limiter.Limit(cat)
		logging.FromContext(ctx).Warn("rate limit exceeded",
			zap.String("method", method),
			zap.String("category", string(cat)),
			zap.String("identity", id),
			zap.Int("requests_per_minute", cfg.RequestsPerMinute),
			zap.Int("burst", cfg.Burst),
		)
		return status.Errorf(codes.ResourceExhausted,
			"rate limit exceeded (%s: %d/min, burst %d), please slow down",
			cat, cfg.RequestsPerMinute, cfg.Burst)
	}

	return nil
}

// classify resolves the rate-limit Category for a full method name: an explicit
// methodCategories entry wins, then exemptPrefixes, then a name-based heuristic
// (Get/List/Search are CategoryRead), otherwise CategoryDefault.
func classify(method string) Category {
	if cat, ok := methodCategories[method]; ok {
		return cat
	}

	for _, prefix := range exemptPrefixes {
		if strings.HasPrefix(method, prefix) {
			return CategoryExempt
		}
	}

	name := method
	if idx := strings.LastIndex(method, "/"); idx >= 0 {
		name = method[idx+1:]
	}

	if strings.HasPrefix(name, "Get") || strings.HasPrefix(name, "List") || strings.HasPrefix(name, "Search") {
		return CategoryRead
	}

	return CategoryDefault
}

// identity returns the rate-limit bucket key for the caller: the authenticated
// user ID when present ("u:<id>"), else the client IP ("ip:<addr>"), else
// "anon" so all unidentifiable callers share one bucket.
func identity(ctx context.Context) string {
	if userID := interceptor.GetUserID(ctx); userID != "" {
		return "u:" + userID
	}
	if ip := clientIP(ctx); ip != "" {
		return "ip:" + ip
	}
	return "anon"
}

// clientIP extracts the caller's IP, preferring the first address in
// x-forwarded-for or x-real-ip metadata (proxy-provided) and falling back to
// the gRPC peer address. It returns "" when no address can be determined.
func clientIP(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, key := range []string{"x-forwarded-for", "x-real-ip"} {
			if vals := md.Get(key); len(vals) > 0 && vals[0] != "" {
				return strings.TrimSpace(strings.Split(vals[0], ",")[0])
			}
		}
	}

	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
			return host
		}
		return p.Addr.String()
	}

	return ""
}
