package auth

import (
	"strings"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/auth/oauth"
)

func TestSanitizeHandle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Alice@example.com", "alice"},
		{"a.b-c_d@x.com", "a.b-c_d"},
		{"UP", ""},       // shorter than 3 after sanitize
		{"名前@x.com", ""}, // non-ascii stripped -> empty
		{"weird!!name@x.com", "weirdname"},
		{strings.Repeat("a", 40) + "@x.com", strings.Repeat("a", 32)}, // clamped to 32
	}
	for _, c := range cases {
		if got := sanitizeHandle(c.in); got != c.want {
			t.Errorf("sanitizeHandle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestListAuthMethodsPasswordOnly(t *testing.T) {
	s := &Service{} // no oauth manager
	methods := s.ListAuthMethods()
	if len(methods) != 1 || methods[0].ID != "password" || methods[0].Type != "password" {
		t.Fatalf("methods = %+v", methods)
	}
}

func TestListAuthMethodsWithProvider(t *testing.T) {
	orig := oauth.Registry
	oauth.Registry = append(append([]oauth.ProviderDef(nil), orig...), oauth.ProviderDef{
		Name: "tprov", EnvPrefix: "OAUTH_TPROV", DisplayName: "TProv", Icon: "tp",
		// no Issuer => available on presence, no network needed
	})
	t.Cleanup(func() { oauth.Registry = orig })

	mgr := oauth.NewManager(map[string]oauth.Credentials{"tprov": {ClientID: "id"}}, nil, nil)
	s := &Service{oauth: mgr}

	methods := s.ListAuthMethods()
	if len(methods) != 2 {
		t.Fatalf("expected password + tprov, got %+v", methods)
	}
	if methods[0].ID != "password" {
		t.Errorf("first method = %q, want password", methods[0].ID)
	}
	p := methods[1]
	if p.ID != "tprov" || p.Type != "oauth" || p.DisplayName != "TProv" || p.Icon != "tp" || p.BeginPath != "/v1/auth/oauth/begin" {
		t.Errorf("oauth method = %+v", p)
	}
}
