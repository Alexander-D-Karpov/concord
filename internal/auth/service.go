package auth

import (
	"context"
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

// Service implements the authentication business logic: registration, password
// and OAuth login, and token issue/refresh/revoke. It depends on the users
// repository, a Postgres pool for refresh-token storage, the JWT and OAuth
// managers, and an optional user cache.
type Service struct {
	usersRepo  *users.Repository
	pool       *pgxpool.Pool
	jwt        *jwt.Manager
	oauth      *oauth.Manager
	cache      *cache.Cache
	lockout    *LockoutManager
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// Tokens is the credential set returned to a client on successful auth. ExpiresIn
// is the access-token lifetime in seconds; the refresh token lives longer.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
}

// NewService wires a Service from its dependencies, taking the access and refresh
// token TTLs from cfg. cacheClient may be nil, in which case user caching is skipped.
func NewService(
	usersRepo *users.Repository,
	pool *pgxpool.Pool,
	jwtMgr *jwt.Manager,
	oauthMgr *oauth.Manager,
	cacheClient *cache.Cache,
	cfg config.AuthConfig,
) *Service {
	s := &Service{
		usersRepo:  usersRepo,
		pool:       pool,
		jwt:        jwtMgr,
		oauth:      oauthMgr,
		cache:      cacheClient,
		accessTTL:  cfg.JWTExpiration,
		refreshTTL: cfg.RefreshExpiration,
	}
	// Brute-force lockout requires the cache to hold counters; it is enabled only
	// when a cache is present and a positive attempt limit is configured.
	if cacheClient != nil && cfg.LoginMaxAttempts > 0 {
		s.lockout = NewLockoutManager(cacheClient, cfg.LoginMaxAttempts, cfg.LoginLockoutPeriod, cfg.LoginAttemptWindow)
	}
	return s
}

// Register validates the handle (3-32 chars) and password (min 6 chars), rejects
// an already-taken handle with a Conflict, bcrypt-hashes the password, creates the
// user, and returns a fresh token pair. The plaintext password is never stored.
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

// LoginPassword authenticates by handle and password, returning a token pair on
// success. It returns the same generic Unauthorized ("invalid credentials") for an
// unknown handle, an OAuth-only account with no password, and a wrong password, so
// callers cannot distinguish which check failed.
func (s *Service) LoginPassword(ctx context.Context, handle, password string) (*Tokens, error) {
	// Reject early when the identifier is locked out. The counter is keyed on the
	// submitted handle regardless of whether the account exists, so brute-forcing a
	// single handle is throttled without revealing whether it exists.
	if s.lockout != nil {
		if locked, err := s.lockout.IsLocked(ctx, handle); err == nil && locked {
			return nil, apperr.TooManyRequests("too many failed login attempts, try again later")
		}
	}

	user, err := s.usersRepo.GetByHandle(ctx, handle)
	if err != nil {
		s.recordLoginFailure(ctx, handle)
		return nil, apperr.Unauthorized("invalid credentials")
	}

	if user.PasswordHash == nil || *user.PasswordHash == "" {
		s.recordLoginFailure(ctx, handle)
		return nil, apperr.Unauthorized("invalid credentials")
	}

	if bcrypt.CompareHashAndPassword([]byte(*user.PasswordHash), []byte(password)) != nil {
		s.recordLoginFailure(ctx, handle)
		return nil, apperr.Unauthorized("invalid credentials")
	}

	if s.lockout != nil {
		_ = s.lockout.ClearAttempts(ctx, handle)
	}

	return s.issueTokens(ctx, user)
}

// recordLoginFailure counts a failed login attempt for handle when lockout is
// enabled, tripping a lock once the configured threshold is reached. Cache errors
// are ignored so a cache blip never blocks a legitimate login.
func (s *Service) recordLoginFailure(ctx context.Context, handle string) {
	if s.lockout != nil {
		_ = s.lockout.RecordFailedAttempt(ctx, handle)
	}
}

// RefreshToken exchanges a valid, unrevoked, unexpired refresh token for a new
// token pair. It verifies the JWT, then checks the token's SHA-256 hash exists in
// the database for that user, revokes the old token (rotation, single use), and
// issues fresh tokens. Any failed check returns Unauthorized.
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

// RevokeRefreshToken marks the token's stored hash as revoked (logout). It is
// idempotent: revoking an unknown or already-revoked token is not an error.
func (s *Service) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	tokenHash := hashToken(refreshToken)
	_, err := s.pool.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = NOW() WHERE token_hash = $1
	`, tokenHash)
	return err
}

// BeginOAuth returns the provider authorization URL and a CSRF state value the
// client must echo back on callback. It returns BadRequest if OAuth is unconfigured.
func (s *Service) BeginOAuth(ctx context.Context, provider, redirectURI string) (string, string, error) {
	if s.oauth == nil {
		return "", "", apperr.BadRequest("OAuth not configured")
	}
	return s.oauth.GetAuthURL(provider, redirectURI)
}

// CompleteOAuth exchanges the authorization code for provider user info and returns
// a token pair. If no local user is linked to that provider identity it lazily
// creates one (using the provider email as handle). Returns BadRequest if OAuth is
// unconfigured. The caller is responsible for having verified the CSRF state first.
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

// issueTokens generates an access/refresh pair for the user, persists the refresh
// token's SHA-256 hash (never the token itself) with its expiry, and best-effort
// caches the user for 5 minutes. Storing only the hash means a database leak does
// not expose usable refresh tokens.
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
