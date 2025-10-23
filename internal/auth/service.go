package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/auth/oauth"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	apperr "github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	usersRepo  *users.Repository
	pool       *pgxpool.Pool
	jwt        *jwt.Manager
	oauth      *oauth.Manager
	cache      *cache.Cache
	accessTTL  time.Duration
	refreshTTL time.Duration
}

type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
}

func NewService(
	usersRepo *users.Repository,
	pool *pgxpool.Pool,
	jwtMgr *jwt.Manager,
	oauthMgr *oauth.Manager,
	cacheClient *cache.Cache,
	cfg config.AuthConfig,
) *Service {
	return &Service{
		usersRepo:  usersRepo,
		pool:       pool,
		jwt:        jwtMgr,
		oauth:      oauthMgr,
		cache:      cacheClient,
		accessTTL:  cfg.JWTExpiration,
		refreshTTL: cfg.RefreshExpiration,
	}
}

func (s *Service) Register(ctx context.Context, handle, password, displayName string) (*Tokens, error) {
	if len(handle) < 3 || len(handle) > 32 {
		return nil, apperr.BadRequest("handle must be 3-32 characters")
	}
	if len(password) < 6 {
		return nil, apperr.BadRequest("password must be at least 6 characters")
	}

	if _, err := s.usersRepo.GetByHandle(ctx, handle); err == nil {
		return nil, apperr.Conflict("handle already taken")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, apperr.Internal("failed to hash password", err)
	}

	hashStr := string(hash)
	user := &users.User{
		ID:           uuid.New(),
		Handle:       handle,
		DisplayName:  displayName,
		PasswordHash: &hashStr,
	}

	if err := s.usersRepo.Create(ctx, user); err != nil {
		return nil, apperr.Internal("failed to create user", err)
	}

	return s.issueTokens(ctx, user)
}

func (s *Service) LoginPassword(ctx context.Context, handle, password string) (*Tokens, error) {
	user, err := s.usersRepo.GetByHandle(ctx, handle)
	if err != nil {
		return nil, apperr.Unauthorized("invalid credentials")
	}

	if user.PasswordHash == nil || *user.PasswordHash == "" {
		return nil, apperr.Unauthorized("invalid credentials")
	}

	if bcrypt.CompareHashAndPassword([]byte(*user.PasswordHash), []byte(password)) != nil {
		return nil, apperr.Unauthorized("invalid credentials")
	}

	return s.issueTokens(ctx, user)
}

func (s *Service) RefreshToken(ctx context.Context, refreshToken string) (*Tokens, error) {
	claims, err := s.jwt.ValidateRefreshToken(refreshToken)
	if err != nil {
		return nil, apperr.Unauthorized("invalid refresh token")
	}

	tokenHash := hashToken(refreshToken)

	var exists bool
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM refresh_tokens 
			WHERE token_hash = $1 AND user_id = $2 AND expires_at > NOW() AND revoked_at IS NULL
		)
	`, tokenHash, claims.UserID).Scan(&exists)

	if err != nil || !exists {
		return nil, apperr.Unauthorized("invalid refresh token")
	}

	_, err = s.pool.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = NOW() WHERE token_hash = $1
	`, tokenHash)
	if err != nil {
		return nil, apperr.Internal("failed to revoke old token", err)
	}

	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		return nil, apperr.Unauthorized("invalid user id")
	}

	user, err := s.usersRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, apperr.Unauthorized("user not found")
	}

	return s.issueTokens(ctx, user)
}

func (s *Service) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	tokenHash := hashToken(refreshToken)
	_, err := s.pool.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = NOW() WHERE token_hash = $1
	`, tokenHash)
	return err
}

func (s *Service) BeginOAuth(ctx context.Context, provider, redirectURI string) (string, string, error) {
	if s.oauth == nil {
		return "", "", apperr.BadRequest("OAuth not configured")
	}
	return s.oauth.GetAuthURL(provider, redirectURI)
}

func (s *Service) CompleteOAuth(ctx context.Context, provider, code, redirectURI string) (*Tokens, error) {
	if s.oauth == nil {
		return nil, apperr.BadRequest("OAuth not configured")
	}

	userInfo, err := s.oauth.ExchangeCode(ctx, provider, code, redirectURI)
	if err != nil {
		return nil, apperr.Internal("OAuth exchange failed", err)
	}

	user, err := s.usersRepo.GetByOAuth(ctx, provider, userInfo.ID)
	if err != nil {
		user = &users.User{
			ID:            uuid.New(),
			Handle:        userInfo.Email,
			DisplayName:   userInfo.Name,
			AvatarURL:     userInfo.Picture,
			OAuthProvider: &provider,
			OAuthSubject:  &userInfo.ID,
		}
		if err := s.usersRepo.Create(ctx, user); err != nil {
			return nil, apperr.Internal("failed to create user", err)
		}
	}

	return s.issueTokens(ctx, user)
}

func (s *Service) issueTokens(ctx context.Context, user *users.User) (*Tokens, error) {
	accessToken, err := s.jwt.GenerateAccessToken(user.ID.String(), user.Handle, s.accessTTL)
	if err != nil {
		return nil, apperr.Internal("failed to generate access token", err)
	}

	refreshToken, err := s.jwt.GenerateRefreshToken(user.ID.String(), s.refreshTTL)
	if err != nil {
		return nil, apperr.Internal("failed to generate refresh token", err)
	}

	tokenHash := hashToken(refreshToken)
	expiresAt := time.Now().Add(s.refreshTTL)

	_, err = s.pool.Exec(ctx, `
		INSERT INTO refresh_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (token_hash) DO NOTHING
	`, tokenHash, user.ID, expiresAt)

	if err != nil {
		return nil, apperr.Internal("failed to store refresh token", err)
	}

	if s.cache != nil {
		_ = s.cache.Set(ctx, "user:"+user.ID.String(), user, 5*time.Minute)
	}

	return &Tokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(s.accessTTL.Seconds()),
	}, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
