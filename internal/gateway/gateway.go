package gateway

import (
	"context"
	"fmt"
	"net/http"
	"time"

	adminv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/admin/v1"
	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	chatv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/chat/v1"
	dmv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/dm/v1"
	featuresv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/features/v1"
	friendsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/friends/v1"
	membershipv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/membership/v1"
	pushv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/push/v1"
	registryv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/registry/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	unfurlv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/unfurl/v1"
	usersv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/users/v1"
	"github.com/Alexander-D-Karpov/concord/internal/middleware"
	"github.com/Alexander-D-Karpov/concord/internal/version"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

// Gateway is the HTTP/JSON front end that proxies REST requests to the gRPC
// backend via grpc-gateway. Init must be called before the handler is usable.
type Gateway struct {
	grpcAddr string
	logger   *zap.Logger
	handler  http.Handler
	dialOpts []grpc.DialOption
}

// New creates a Gateway that dials the gRPC server at grpcAddr. When no dialOpts
// are supplied it defaults to an insecure (plaintext) connection, suitable for
// same-host/in-cluster use.
func New(grpcAddr string, logger *zap.Logger, dialOpts ...grpc.DialOption) *Gateway {
	if len(dialOpts) == 0 {
		dialOpts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}

	return &Gateway{
		grpcAddr: grpcAddr,
		logger:   logger,
		dialOpts: dialOpts,
	}
}

// Init builds the grpc-gateway mux, registers every service handler against the
// gRPC endpoint, and wraps the mux with the middleware chain applied outermost
// first: compression, then CORS, then version header, then request logging. It
// returns an error if any service handler fails to register.
func (g *Gateway) Init(ctx context.Context) error {
	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(customMatcher),
		runtime.WithErrorHandler(customErrorHandler),
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions: protojson.MarshalOptions{
				// camelCase field names, matching the gRPC/protojson default so the
				// HTTP gateway and native gRPC clients see the same JSON shape.
				UseProtoNames:   false,
				EmitUnpopulated: true,
			},
			UnmarshalOptions: protojson.UnmarshalOptions{
				DiscardUnknown: true,
			},
		}),
	)

	handlers := []func(context.Context, *runtime.ServeMux, string, []grpc.DialOption) error{
		adminv1.RegisterAdminServiceHandlerFromEndpoint,
		authv1.RegisterAuthServiceHandlerFromEndpoint,
		usersv1.RegisterUsersServiceHandlerFromEndpoint,
		roomsv1.RegisterRoomsServiceHandlerFromEndpoint,
		chatv1.RegisterChatServiceHandlerFromEndpoint,
		membershipv1.RegisterMembershipServiceHandlerFromEndpoint,
		callv1.RegisterCallServiceHandlerFromEndpoint,
		registryv1.RegisterRegistryServiceHandlerFromEndpoint,
		friendsv1.RegisterFriendsServiceHandlerFromEndpoint,
		dmv1.RegisterDMServiceHandlerFromEndpoint,
		unfurlv1.RegisterUnfurlServiceHandlerFromEndpoint,
		featuresv1.RegisterFeaturesServiceHandlerFromEndpoint,
		pushv1.RegisterPushServiceHandlerFromEndpoint,
	}

	for _, register := range handlers {
		if err := register(ctx, mux, g.grpcAddr, g.dialOpts); err != nil {
			return fmt.Errorf("register handler: %w", err)
		}
	}

	g.handler = middleware.CompressionMiddleware(
		corsMiddleware(
			versionMiddleware(
				loggingMiddleware(mux, g.logger),
			),
		),
	)

	return nil
}

// versionMiddleware sets the X-Concord-Version response header on every request
// before delegating to next.
func versionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Concord-Version", version.API())
		next.ServeHTTP(w, r)
	})
}

// ServeHTTP dispatches to the wrapped handler built by Init, letting Gateway
// satisfy http.Handler. It panics if called before Init.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.handler.ServeHTTP(w, r)
}

// Start runs the HTTP server on port with fixed read/write/idle timeouts and
// blocks until it fails or ctx is cancelled, in which case it shuts down with a
// 5s grace period. A clean ErrServerClosed is not reported as an error.
func (g *Gateway) Start(ctx context.Context, port int) error {
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      g,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	g.logger.Info("HTTP gateway starting", zap.Int("port", port))

	errChan := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// customMatcher decides which HTTP request headers are forwarded to the gRPC
// backend as metadata. It allow-lists auth, tracing, rate-limit-bypass, and
// client-IP headers, and defers all others to the default matcher.
func customMatcher(key string) (string, bool) {
	switch key {
	case "authorization", "x-request-id", "x-correlation-id", "grpc-timeout",
		"x-concord-ratelimit-bypass", "x-forwarded-for", "x-real-ip":
		return key, true
	default:
		return runtime.DefaultHeaderMatcher(key)
	}
}

// customErrorHandler currently delegates to grpc-gateway's default HTTP error
// handler; it exists as the hook point for customizing error responses.
func customErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, err)
}

// corsMiddleware adds permissive CORS headers (any origin) and short-circuits
// OPTIONS preflight requests with 204 before they reach next.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID, Grpc-Timeout")
		w.Header().Set("Access-Control-Expose-Headers", "Grpc-Metadata-*")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs each HTTP request after it completes, capturing the
// response status via a responseWriter wrapper along with method, path,
// duration, and remote address.
func loggingMiddleware(next http.Handler, logger *zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		logger.Info("http request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", wrapped.statusCode),
			zap.Duration("duration", time.Since(start)),
			zap.String("remote_addr", r.RemoteAddr),
		)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code written
// by the handler so loggingMiddleware can record it.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader records the status code before forwarding it to the underlying
// ResponseWriter.
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Shutdown is a no-op that returns nil; the HTTP server is instead stopped by
// cancelling the context passed to Start. It exists to satisfy the lifecycle
// interface expected by callers.
func (g *Gateway) Shutdown(ctx context.Context) error {
	return nil
}
