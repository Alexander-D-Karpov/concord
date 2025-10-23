package auth

import (
	"context"

	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Handler struct {
	authv1.UnimplementedAuthServiceServer
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

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

func (h *Handler) Logout(ctx context.Context, req *authv1.LogoutRequest) (*authv1.EmptyResponse, error) {
	if req.GetRefreshToken() == "" {
		return &authv1.EmptyResponse{}, nil
	}

	if err := h.svc.RevokeRefreshToken(ctx, req.GetRefreshToken()); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &authv1.EmptyResponse{}, nil
}

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

func (h *Handler) LoginOAuth(ctx context.Context, req *authv1.LoginOAuthRequest) (*authv1.Token, error) {
	if req.GetProvider() == "" || req.GetCode() == "" {
		return nil, status.Error(codes.InvalidArgument, "provider and code are required")
	}

	tokens, err := h.svc.CompleteOAuth(ctx, req.GetProvider(), req.GetCode(), req.GetRedirectUri())
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
