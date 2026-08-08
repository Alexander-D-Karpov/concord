package auth

import (
	"context"

	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Handler adapts the auth Service to the gRPC AuthService interface, translating
// requests and mapping app errors to gRPC status codes via errors.ToGRPCError.
type Handler struct {
	authv1.UnimplementedAuthServiceServer
	svc *Service
}

// NewHandler returns a Handler backed by the given Service.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// ListAuthMethods handles the ListAuthMethods RPC, returning the login methods the
// server currently offers (password plus any available OAuth providers). It is
// public so clients can render the login screen before authenticating.
func (h *Handler) ListAuthMethods(_ context.Context, _ *authv1.ListAuthMethodsRequest) (*authv1.ListAuthMethodsResponse, error) {
	methods := h.svc.ListAuthMethods()
	out := make([]*authv1.AuthMethod, 0, len(methods))
	for _, m := range methods {
		out = append(out, &authv1.AuthMethod{
			Id:          m.ID,
			Type:        m.Type,
			DisplayName: m.DisplayName,
			Icon:        m.Icon,
			BeginPath:   m.BeginPath,
		})
	}
	return &authv1.ListAuthMethodsResponse{Methods: out}, nil
}

// Register handles the Register RPC: it requires handle and password, defaults the
// display name to the handle when empty, and returns a Bearer token pair.
func (h *Handler) Register(ctx context.Context, req *authv1.RegisterRequest) (*authv1.Token, error) {
	if req.GetHandle() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "handle and password are required")
	}

	displayName := req.GetDisplayName()
	if displayName == "" {
		displayName = req.GetHandle()
	}

	tokens, err := h.svc.Register(ctx, req.GetHandle(), req.GetPassword(), displayName)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &authv1.Token{
		AccessToken:  tokens.AccessToken,
		ExpiresIn:    tokens.ExpiresIn,
		RefreshToken: tokens.RefreshToken,
		TokenType:    "Bearer",
	}, nil
}

// LoginPassword handles the LoginPassword RPC, requiring handle and password and
// returning a Bearer token pair on success.
func (h *Handler) LoginPassword(ctx context.Context, req *authv1.LoginPasswordRequest) (*authv1.Token, error) {
	if req.GetHandle() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "handle and password are required")
	}

	tokens, err := h.svc.LoginPassword(ctx, req.GetHandle(), req.GetPassword())
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &authv1.Token{
		AccessToken:  tokens.AccessToken,
		ExpiresIn:    tokens.ExpiresIn,
		RefreshToken: tokens.RefreshToken,
		TokenType:    "Bearer",
	}, nil
}

// Refresh handles the Refresh RPC, rotating the supplied refresh token for a new
// Bearer token pair. Requires a non-empty refresh_token.
func (h *Handler) Refresh(ctx context.Context, req *authv1.RefreshRequest) (*authv1.Token, error) {
	if req.GetRefreshToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh_token is required")
	}

	tokens, err := h.svc.RefreshToken(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &authv1.Token{
		AccessToken:  tokens.AccessToken,
		ExpiresIn:    tokens.ExpiresIn,
		RefreshToken: tokens.RefreshToken,
		TokenType:    "Bearer",
	}, nil
}

// Logout handles the Logout RPC by revoking the given refresh token. An empty
// refresh_token is treated as a successful no-op rather than an error.
func (h *Handler) Logout(ctx context.Context, req *authv1.LogoutRequest) (*authv1.EmptyResponse, error) {
	if req.GetRefreshToken() == "" {
		return &authv1.EmptyResponse{}, nil
	}

	if err := h.svc.RevokeRefreshToken(ctx, req.GetRefreshToken()); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &authv1.EmptyResponse{}, nil
}

// OAuthBegin handles the OAuthBegin RPC, returning the provider authorization URL
// and the CSRF state the client must present on callback. Requires a provider.
func (h *Handler) OAuthBegin(ctx context.Context, req *authv1.OAuthBeginRequest) (*authv1.OAuthBeginResponse, error) {
	if req.GetProvider() == "" {
		return nil, status.Error(codes.InvalidArgument, "provider is required")
	}

	authURL, state, err := h.svc.BeginOAuth(ctx, req.GetProvider(), req.GetRedirectUri())
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &authv1.OAuthBeginResponse{
		AuthUrl: authURL,
		State:   state,
	}, nil
}

// LoginOAuth handles the LoginOAuth RPC, completing the OAuth code exchange and
// returning a Bearer token pair. Requires provider, code, and the state issued by
// OAuthBegin.
func (h *Handler) LoginOAuth(ctx context.Context, req *authv1.LoginOAuthRequest) (*authv1.Token, error) {
	if req.GetProvider() == "" || req.GetCode() == "" || req.GetState() == "" {
		return nil, status.Error(codes.InvalidArgument, "provider, code, and state are required")
	}

	tokens, err := h.svc.CompleteOAuth(ctx, req.GetProvider(), req.GetCode(), req.GetState(), req.GetRedirectUri())
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &authv1.Token{
		AccessToken:  tokens.AccessToken,
		ExpiresIn:    tokens.ExpiresIn,
		RefreshToken: tokens.RefreshToken,
		TokenType:    "Bearer",
	}, nil
}
