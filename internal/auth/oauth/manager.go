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

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"golang.org/x/oauth2"
)

type Manager struct {
	providers map[string]*Provider
}

type Provider struct {
	config *oauth2.Config
}

type UserInfo struct {
	ID      string
	Email   string
	Name    string
	Picture string
}

func NewManager(cfg config.AuthConfig) *Manager {
	m := &Manager{
		providers: make(map[string]*Provider),
	}

	for name, providerCfg := range cfg.OAuth {
		m.providers[name] = &Provider{
			config: &oauth2.Config{
				ClientID:     providerCfg.ClientID,
				ClientSecret: providerCfg.ClientSecret,
				RedirectURL:  providerCfg.RedirectURL,
				Endpoint: oauth2.Endpoint{
					AuthURL:  providerCfg.AuthURL,
					TokenURL: providerCfg.TokenURL,
				},
				Scopes: getScopes(name),
			},
		}
	}

	return m
}

func (m *Manager) GetAuthURL(provider, redirectURI string) (string, string, error) {
	p, exists := m.providers[provider]
	if !exists {
		return "", "", fmt.Errorf("provider %s not configured", provider)
	}

	state := generateState()

	if redirectURI != "" {
		cfg := *p.config
		cfg.RedirectURL = redirectURI
		return cfg.AuthCodeURL(state), state, nil
	}

	return p.config.AuthCodeURL(state), state, nil
}

func (m *Manager) ExchangeCode(ctx context.Context, provider, code, redirectURI string) (*UserInfo, error) {
	p, exists := m.providers[provider]
	if !exists {
		return nil, fmt.Errorf("provider %s not configured", provider)
	}

	cfg := p.config
	if redirectURI != "" {
		cfg = &oauth2.Config{
			ClientID:     p.config.ClientID,
			ClientSecret: p.config.ClientSecret,
			RedirectURL:  redirectURI,
			Endpoint:     p.config.Endpoint,
			Scopes:       p.config.Scopes,
		}
	}

	token, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("code exchange failed: %w", err)
	}

	return m.fetchUserInfo(ctx, provider, token.AccessToken)
}

func (m *Manager) fetchUserInfo(ctx context.Context, provider, accessToken string) (*UserInfo, error) {
	var userInfoURL string
	switch provider {
	case "google":
		userInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	case "github":
		userInfoURL = "https://api.github.com/user"
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", userInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			fmt.Println("error closing response body:", err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user info request failed: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parseUserInfo(provider, body)
}

func parseUserInfo(provider string, data []byte) (*UserInfo, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	info := &UserInfo{}

	switch provider {
	case "google":
		info.ID = getString(raw, "id")
		info.Email = getString(raw, "email")
		info.Name = getString(raw, "name")
		info.Picture = getString(raw, "picture")
	case "github":
		info.ID = fmt.Sprintf("%v", raw["id"])
		info.Email = getString(raw, "email")
		info.Name = getString(raw, "name")
		if info.Name == "" {
			info.Name = getString(raw, "login")
		}
		info.Picture = getString(raw, "avatar_url")
	}

	if info.Email == "" {
		info.Email = fmt.Sprintf("%s_%s@oauth", provider, info.ID)
	}

	return info, nil
}

func getString(m map[string]interface{}, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

func getScopes(provider string) []string {
	switch provider {
	case "google":
		return []string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		}
	case "github":
		return []string{"read:user", "user:email"}
	default:
		return []string{}
	}
}

func generateState() string {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(b)
}

func ValidateState(state, expected string) bool {
	return state != "" && state == expected
}

func BuildCallbackURL(baseURL, provider string) string {
	u, _ := url.Parse(baseURL)
	u.Path = fmt.Sprintf("/auth/%s/callback", provider)
	return u.String()
}
