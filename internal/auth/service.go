package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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

// oauthStateTTL bounds how long an in-flight OAuth login (state → PKCE verifier)
// stays valid between OAuthBegin and the code exchange.
const oauthStateTTL = 10 * time.Minute

// oauthFlowState is the server-side record for one in-flight OAuth login, stored
// in Redis under the opaque state value and consumed once at exchange.
type oauthFlowState struct {
	Provider    string `json:"provider"`
	RedirectURI string `json:"redirect_uri"`
	Verifier    string `json:"verifier"`
}

// AuthMethod is a login option advertised to clients by ListAuthMethods.
type AuthMethod struct {
	ID          string
	Type        string
	DisplayName string
	Icon        string
	BeginPath   string
}

func oauthStateKey(state string) string { return "oauth:state:" + state }

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

	// Avatar ingestion for OAuth signups (optional; enabled via SetAvatarIngestion).
	avatarStorePath string
	avatarStoreURL  string
	httpClient      *http.Client
}

// SetAvatarIngestion enables downloading an OAuth provider's profile picture on
// first login and storing it in local avatar storage (same pipeline as uploaded
// avatars), so the account's avatar is served by Concord rather than linking to an
// external URL. When left unset, OAuth signups get no avatar.
func (s *Service) SetAvatarIngestion(storagePath, storageURL string) {
	s.avatarStorePath = storagePath
	s.avatarStoreURL = storageURL
	if s.httpClient == nil {
		s.httpClient = &http.Client{Timeout: 15 * time.Second}
	}
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

// ListAuthMethods returns the login methods the server currently offers: password
// first, then every OAuth provider that is configured and available.
func (s *Service) ListAuthMethods() []AuthMethod {
	methods := []AuthMethod{
		{ID: "password", Type: "password", DisplayName: "Password"},
	}
	if s.oauth != nil {
		for _, p := range s.oauth.Available() {
			methods = append(methods, AuthMethod{
				ID:          p.Name,
				Type:        "oauth",
				DisplayName: p.DisplayName,
				Icon:        p.Icon,
				BeginPath:   "/v1/auth/oauth/begin",
			})
		}
	}
	return methods
}

// BeginOAuth starts a PKCE authorization-code login. It generates a state and a
// PKCE verifier, persists {provider, redirectURI, verifier} in Redis under the
// state (single-use, oauthStateTTL), and returns the provider authorization URL
// (carrying the S256 challenge) plus the state the client must echo back. Errors
// are BadRequest when OAuth/Redis is unavailable, the provider is unavailable, or
// the redirect URI is not allowed.
func (s *Service) BeginOAuth(ctx context.Context, provider, redirectURI string) (string, string, error) {
	if s.oauth == nil {
		return "", "", apperr.BadRequest("OAuth not configured")
	}
	if s.cache == nil {
		return "", "", apperr.BadRequest("OAuth requires Redis, which is disabled")
	}
	if !s.oauth.IsAvailable(provider) {
		return "", "", apperr.BadRequest("OAuth provider not available")
	}
	if redirectURI == "" {
		redirectURI = s.oauth.DefaultRedirect(provider)
	}
	if redirectURI == "" {
		return "", "", apperr.BadRequest("redirect_uri is required")
	}
	if !s.oauth.RedirectAllowed(provider, redirectURI) {
		return "", "", apperr.BadRequest("redirect_uri is not allowed")
	}

	state := oauth.GenerateState()
	if state == "" {
		return "", "", apperr.Internal("failed to generate state", nil)
	}
	verifier := oauth.GenerateVerifier()

	authURL, err := s.oauth.BuildAuthURL(provider, redirectURI, state, verifier)
	if err != nil {
		return "", "", apperr.BadRequest(err.Error())
	}

	fs := oauthFlowState{Provider: provider, RedirectURI: redirectURI, Verifier: verifier}
	if err := s.cache.Set(ctx, oauthStateKey(state), fs, oauthStateTTL); err != nil {
		return "", "", apperr.Internal("failed to persist oauth state", err)
	}
	return authURL, state, nil
}

// CompleteOAuth finishes a PKCE login. It looks up and consumes the state (one
// time), verifies it was issued for this provider and redirect URI, exchanges the
// code (with the stored verifier) for the provider profile, then finds or lazily
// creates the linked account and issues a token pair. A missing/expired/replayed
// state or a failed exchange is Unauthorized.
func (s *Service) CompleteOAuth(ctx context.Context, provider, code, state, redirectURI string) (*Tokens, error) {
	if s.oauth == nil {
		return nil, apperr.BadRequest("OAuth not configured")
	}
	if s.cache == nil {
		return nil, apperr.BadRequest("OAuth requires Redis, which is disabled")
	}
	if state == "" {
		return nil, apperr.Unauthorized("missing oauth state")
	}

	var fs oauthFlowState
	if err := s.cache.Get(ctx, oauthStateKey(state), &fs); err != nil {
		return nil, apperr.Unauthorized("invalid or expired oauth state")
	}
	// Consume the state regardless of the outcome so it cannot be replayed.
	_ = s.cache.Delete(ctx, oauthStateKey(state))

	if fs.Provider != provider {
		return nil, apperr.Unauthorized("oauth state provider mismatch")
	}
	if redirectURI != "" && fs.RedirectURI != redirectURI {
		return nil, apperr.Unauthorized("oauth state redirect mismatch")
	}

	userInfo, err := s.oauth.Exchange(ctx, provider, code, fs.RedirectURI, fs.Verifier)
	if err != nil {
		return nil, apperr.Unauthorized("oauth exchange failed")
	}

	user, err := s.usersRepo.GetByOAuth(ctx, provider, userInfo.ID)
	if err != nil {
		if !apperr.IsNotFound(err) {
			return nil, apperr.Internal("failed to look up oauth user", err)
		}
		handle, herr := s.uniqueHandle(ctx, userInfo.Email, userInfo.Name)
		if herr != nil {
			return nil, apperr.Internal("failed to allocate handle", herr)
		}
		displayName := userInfo.Name
		if displayName == "" {
			displayName = handle
		}
		user = &users.User{
			ID:            uuid.New(),
			Handle:        handle,
			DisplayName:   displayName,
			OAuthProvider: &provider,
			OAuthSubject:  &userInfo.ID,
		}
		// Best-effort: ingest the provider avatar into local storage so it is served
		// by Concord rather than linked externally. On any failure the account is
		// still created, just without an avatar.
		if userInfo.Picture != "" && s.avatarStorePath != "" {
			if full, thumb, ierr := s.ingestAvatar(ctx, user.ID.String(), userInfo.Picture); ierr == nil {
				user.AvatarURL = full
				user.AvatarThumbnailURL = thumb
			}
		}
		if err := s.usersRepo.Create(ctx, user); err != nil {
			return nil, apperr.Internal("failed to create user", err)
		}
	}

	return s.issueTokens(ctx, user)
}

// uniqueHandle derives a valid, unused handle from the email local-part (falling
// back to name, then "user") and appends a numeric suffix until it finds one no
// account holds. It keeps candidates within the 3-32 char handle limit.
func (s *Service) uniqueHandle(ctx context.Context, email, name string) (string, error) {
	base := sanitizeHandle(email)
	if base == "" {
		base = sanitizeHandle(name)
	}
	if base == "" {
		base = "user"
	}

	candidate := base
	for i := 2; i < 100000; i++ {
		_, err := s.usersRepo.GetByHandle(ctx, candidate)
		if err != nil {
			if apperr.IsNotFound(err) {
				return candidate, nil
			}
			return "", err
		}
		suffix := strconv.Itoa(i)
		trimmed := base
		if max := 32 - len(suffix); len(trimmed) > max {
			trimmed = trimmed[:max]
		}
		candidate = trimmed + suffix
	}
	return "", fmt.Errorf("could not allocate unique handle for %q", base)
}

// sanitizeHandle reduces s to a candidate handle: the part before any "@",
// lowercased, keeping only [a-z0-9_.-] and clamped to 32 chars. It returns "" when
// the result is shorter than the 3-char minimum so the caller can fall back.
func sanitizeHandle(s string) string {
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(s)

	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 32 {
		out = out[:32]
	}
	if len(out) < 3 {
		return ""
	}
	return out
}

// ingestAvatar downloads the provider profile image at imageURL, runs it through
// the shared avatar pipeline (validate, downscale, re-encode as full + thumbnail
// JPEGs, stripping metadata), stores the files under the avatar storage path, and
// returns their public URLs. The download size is bounded; it is best-effort, so
// callers ignore the error and simply leave the avatar unset on failure.
func (s *Service) ingestAvatar(ctx context.Context, userID, imageURL string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("avatar fetch failed: %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, users.MaxAvatarBytes+1))
	if err != nil {
		return "", "", err
	}

	processed, err := users.ProcessAvatarImage(data)
	if err != nil {
		return "", "", err
	}
	fullRel, thumbRel, err := users.SaveAvatarFiles(s.avatarStorePath, userID, processed.FullData, processed.ThumbData)
	if err != nil {
		return "", "", err
	}
	return s.avatarStoreURL + "/" + fullRel, s.avatarStoreURL + "/" + thumbRel, nil
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
