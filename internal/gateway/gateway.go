package gateway

import (
	"context"
	"fmt"
	"net/http"
	"time"

	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	chatv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/chat/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	usersv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/users/v1"
	"github.com/Alexander-D-Karpov/concord/internal/middleware"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Gateway struct {
	grpcAddr string
	logger   *zap.Logger
	handler  http.Handler
}

func New(grpcAddr string, logger *zap.Logger) *Gateway {
	return &Gateway{
		grpcAddr: grpcAddr,
		logger:   logger,
	}
}

func (g *Gateway) Init(ctx context.Context) error {
	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(customMatcher),
		runtime.WithErrorHandler(customErrorHandler),
	)

	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	if err := authv1.RegisterAuthServiceHandlerFromEndpoint(ctx, mux, g.grpcAddr, opts); err != nil {
		return fmt.Errorf("register auth handler: %w", err)
	}

	if err := usersv1.RegisterUsersServiceHandlerFromEndpoint(ctx, mux, g.grpcAddr, opts); err != nil {
		return fmt.Errorf("register users handler: %w", err)
	}

	if err := roomsv1.RegisterRoomsServiceHandlerFromEndpoint(ctx, mux, g.grpcAddr, opts); err != nil {
		return fmt.Errorf("register rooms handler: %w", err)
	}

	if err := chatv1.RegisterChatServiceHandlerFromEndpoint(ctx, mux, g.grpcAddr, opts); err != nil {
		return fmt.Errorf("register chat handler: %w", err)
	}

	g.handler = middleware.CompressionMiddleware(
		corsMiddleware(
			loggingMiddleware(mux, g.logger),
		),
	)

	return nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.handler.ServeHTTP(w, r)
}

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

func customMatcher(key string) (string, bool) {
	switch key {
	case "authorization", "x-request-id", "x-correlation-id":
		return key, true
	default:
		return runtime.DefaultHeaderMatcher(key)
	}
}

func customErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, err)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

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

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (g *Gateway) Shutdown(ctx context.Context) error {
	return nil
}
