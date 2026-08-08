package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

// Manager holds the configured OAuth2 providers and drives the PKCE
// authorization-code flow for each. The client secret and PKCE verifier never
// leave the server: the server generates the verifier, embeds the S256 challenge
// in the auth URL, and presents the verifier itself at token exchange.
type Manager struct {
	providers map[string]*provider
	http      *http.Client
	logger    *zap.Logger
}

// provider wraps a single provider's static definition, its oauth2 client config,
// its redirect-URL allowlist, and its current availability (guarded by mu).
type provider struct {
	def          ProviderDef
	cfg          *oauth2.Config
	redirectURLs []string

	mu        sync.RWMutex
	available bool
}

// UserInfo is the provider-agnostic profile extracted from a provider's user-info
// endpoint. ID is the provider's stable subject identifier.
type UserInfo struct {
	ID      string
	Email   string
	Name    string
	Picture string
}

// ProviderInfo is the public description of an available provider, used to build
// the auth-methods list shown to clients.
type ProviderInfo struct {
	Name        string
	DisplayName string
	Icon        string
}

// NewManager builds a Manager from configured credentials, combining each with its
// catalog definition from the Registry. Credentials for an unknown provider are
// ignored with a warning. httpClient and logger may be nil.
func NewManager(creds map[string]Credentials, httpClient *http.Client, logger *zap.Logger) *Manager {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	m := &Manager{providers: make(map[string]*provider), http: httpClient, logger: logger}
	for name, c := range creds {
		def, ok := Definition(name)
		if !ok {
			logger.Warn("oauth: credentials for unknown provider ignored", zap.String("provider", name))
			continue
		}
		m.providers[name] = &provider{
			def: def,
			cfg: &oauth2.Config{
				ClientID:     c.ClientID,
				ClientSecret: c.ClientSecret,
				Endpoint:     oauth2.Endpoint{AuthURL: def.AuthURL, TokenURL: def.TokenURL},
				Scopes:       def.Scopes,
			},
			redirectURLs: c.RedirectURLs,
			// Providers with no OIDC issuer are available on presence alone; those
			// with an issuer stay unavailable until RefreshAvailability confirms
			// their discovery document is reachable.
			available: def.Issuer == "",
		}
	}
	return m
}

// StartValidation re-checks provider availability every interval until ctx is
// cancelled. It does not perform an initial check itself — call RefreshAvailability
// once synchronously at startup first — so it is safe to launch in a goroutine.
func (m *Manager) StartValidation(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.RefreshAvailability(ctx)
		}
	}
}

// RefreshAvailability re-evaluates every provider's availability. Providers with an
// OIDC issuer are marked available only if their discovery document is reachable;
// availability transitions are logged. Providers without an issuer are left as-is
// (presence-only).
func (m *Manager) RefreshAvailability(ctx context.Context) {
	for name, p := range m.providers {
		if p.def.Issuer == "" {
			continue
		}
		ok := m.checkDiscovery(ctx, p.def.Issuer)

		p.mu.Lock()
		changed := p.available != ok
		p.available = ok
		p.mu.Unlock()

		if changed {
			if ok {
				m.logger.Info("oauth: provider available", zap.String("provider", name))
			} else {
				m.logger.Warn("oauth: provider withheld (discovery failed)", zap.String("provider", name))
			}
		}
	}
}

// checkDiscovery fetches issuer's OpenID Connect discovery document and reports
// whether it looks valid (reachable, 200, parseable, non-empty issuer/token
// endpoint).
func (m *Manager) checkDiscovery(ctx context.Context, issuer string) bool {
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var doc struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return false
	}
	return doc.Issuer != "" || doc.TokenEndpoint != ""
}

// IsAvailable reports whether the named provider is configured and currently
// available for login.
func (m *Manager) IsAvailable(name string) bool {
	p, ok := m.providers[name]
	if !ok {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.available
}

// Available returns the currently-available providers in Registry order, for
// building the auth-methods list.
func (m *Manager) Available() []ProviderInfo {
	var out []ProviderInfo
	for _, def := range Registry {
		p, ok := m.providers[def.Name]
		if !ok {
			continue
		}
		p.mu.RLock()
		avail := p.available
		p.mu.RUnlock()
		if avail {
			out = append(out, ProviderInfo{Name: def.Name, DisplayName: def.DisplayName, Icon: def.Icon})
		}
	}
	return out
}

// DefaultRedirect returns the provider's first configured redirect URL, or "" when
// none is configured (in which case the client must supply one).
func (m *Manager) DefaultRedirect(name string) string {
	p, ok := m.providers[name]
	if !ok || len(p.redirectURLs) == 0 {
		return ""
	}
	return p.redirectURLs[0]
}

// RedirectAllowed reports whether redirectURI may be used with the named provider.
// A loopback URI (http on 127.0.0.1/::1/localhost, any port) is always allowed per
// RFC 8252 §7.3; any other URI must exactly match one of the provider's configured
// redirect URLs.
func (m *Manager) RedirectAllowed(name, redirectURI string) bool {
	p, ok := m.providers[name]
	if !ok {
		return false
	}
	if isLoopbackRedirect(redirectURI) {
		return true
	}
	for _, a := range p.redirectURLs {
		if a == redirectURI {
			return true
		}
	}
	return false
}

// isLoopbackRedirect reports whether raw is an http redirect to a loopback host on
// any port.
func isLoopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// BuildAuthURL returns the provider authorization URL for the given state, using
// redirectURI for this request and embedding the S256 PKCE challenge derived from
// verifier. Callers must validate redirectURI (via RedirectAllowed) beforehand.
func (m *Manager) BuildAuthURL(name, redirectURI, state, verifier string) (string, error) {
	p, ok := m.providers[name]
	if !ok {
		return "", fmt.Errorf("provider %s not configured", name)
	}
	cfg := *p.cfg
	cfg.RedirectURL = redirectURI
	return cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), nil
}

// Exchange swaps the authorization code for a token — presenting the PKCE verifier
// and the (server-held) client secret — then fetches and maps the user's profile.
// redirectURI must match the one used at BuildAuthURL.
func (m *Manager) Exchange(ctx context.Context, name, code, redirectURI, verifier string) (*UserInfo, error) {
	p, ok := m.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %s not configured", name)
	}
	cfg := *p.cfg
	cfg.RedirectURL = redirectURI

	ctx = context.WithValue(ctx, oauth2.HTTPClient, m.http)
	token, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("code exchange failed: %w", err)
	}
	return m.fetchUserInfo(ctx, p.def, token.AccessToken)
}

// fetchUserInfo calls the provider's user-info endpoint with the bearer access
// token and maps the response via the provider's FieldMap.
func (m *Manager) fetchUserInfo(ctx context.Context, def ProviderDef, accessToken string) (*UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, def.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := m.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user info request failed: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseUserInfo(def, body)
}

// parseUserInfo maps a provider's raw user-info JSON into a UserInfo using its
// FieldMap. When the provider returns no email it synthesizes a stable placeholder
// so downstream account creation always has a value.
func parseUserInfo(def ProviderDef, data []byte) (*UserInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	info := &UserInfo{
		ID:      coerceString(raw[def.Fields.ID]),
		Email:   coerceString(raw[def.Fields.Email]),
		Name:    coerceString(raw[def.Fields.Name]),
		Picture: coerceString(raw[def.Fields.Picture]),
	}
	if info.ID == "" {
		return nil, fmt.Errorf("user info missing id field %q", def.Fields.ID)
	}
	if info.Email == "" {
		info.Email = fmt.Sprintf("%s_%s@oauth.local", def.Name, info.ID)
	}
	return info, nil
}

// coerceString renders a JSON value as a string, handling providers that return
// numeric ids (decoded as float64) or booleans.
func coerceString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// GenerateState returns a URL-safe, base64-encoded 256-bit random CSRF state. It
// returns "" if the system RNG fails; callers must treat "" as an error.
func GenerateState() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// GenerateVerifier returns a fresh PKCE code verifier. ChallengeS256 derives the
// matching S256 challenge. Callers store the verifier server-side and never expose
// it to clients.
func GenerateVerifier() string { return oauth2.GenerateVerifier() }

// ChallengeS256 returns the S256 PKCE challenge for verifier.
func ChallengeS256(verifier string) string { return oauth2.S256ChallengeFromVerifier(verifier) }
