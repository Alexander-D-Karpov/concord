package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// withTestProvider appends a provider definition to the Registry for the duration
// of a test and restores the original catalog on cleanup.
func withTestProvider(t *testing.T, def ProviderDef) {
	t.Helper()
	orig := Registry
	Registry = append(append([]ProviderDef(nil), orig...), def)
	t.Cleanup(func() { Registry = orig })
}

func TestRedirectAllowed(t *testing.T) {
	withTestProvider(t, ProviderDef{Name: "test", EnvPrefix: "OAUTH_TEST"})
	m := NewManager(map[string]Credentials{
		"test": {ClientID: "id", ClientSecret: "sec", RedirectURLs: []string{
			"https://app.example.com/oauth/callback",
			"com.example.app:/oauth2redirect",
		}},
	}, nil, nil)

	cases := []struct {
		uri  string
		want bool
	}{
		{"http://127.0.0.1:54321/callback", true},        // loopback, any port
		{"http://127.0.0.1:1/x", true},                   // loopback, any path
		{"http://localhost:8080/cb", true},               // localhost loopback
		{"http://[::1]:9000/cb", true},                   // ipv6 loopback
		{"https://app.example.com/oauth/callback", true}, // exact allowlist match
		{"com.example.app:/oauth2redirect", true},        // exact custom-scheme match
		{"https://evil.example.com/callback", false},     // not in allowlist
		{"http://10.0.0.5:8080/cb", false},               // non-loopback http
		{"https://127.0.0.1/cb", false},                  // loopback rule is http-only
		{"", false},
	}
	for _, c := range cases {
		if got := m.RedirectAllowed("test", c.uri); got != c.want {
			t.Errorf("RedirectAllowed(%q) = %v, want %v", c.uri, got, c.want)
		}
	}

	if m.RedirectAllowed("unknown", "http://127.0.0.1:1/cb") {
		t.Error("unknown provider should never allow a redirect")
	}
}

func TestBuildAuthURLIncludesPKCE(t *testing.T) {
	withTestProvider(t, ProviderDef{
		Name: "test", EnvPrefix: "OAUTH_TEST",
		AuthURL: "https://provider.example/auth", TokenURL: "https://provider.example/token",
	})
	m := NewManager(map[string]Credentials{"test": {ClientID: "cid", ClientSecret: "sec"}}, nil, nil)

	verifier := GenerateVerifier()
	raw, err := m.BuildAuthURL("test", "http://127.0.0.1:5000/cb", "state-123", verifier)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("state") != "state-123" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("challenge method = %q, want S256", q.Get("code_challenge_method"))
	}
	if got, want := q.Get("code_challenge"), ChallengeS256(verifier); got != want {
		t.Errorf("code_challenge = %q, want %q", got, want)
	}
	if q.Get("client_id") != "cid" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "http://127.0.0.1:5000/cb" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestGenerateStateAndVerifierAreRandom(t *testing.T) {
	s1, s2 := GenerateState(), GenerateState()
	if s1 == "" || s1 == s2 {
		t.Errorf("consecutive states should be non-empty and differ: %q %q", s1, s2)
	}
	v1, v2 := GenerateVerifier(), GenerateVerifier()
	if v1 == "" || v1 == v2 {
		t.Errorf("consecutive verifiers should be non-empty and differ: %q %q", v1, v2)
	}
}

func TestAvailabilityPresenceOnly(t *testing.T) {
	// No Issuer => available on credential presence alone, no network needed.
	withTestProvider(t, ProviderDef{Name: "test", EnvPrefix: "OAUTH_TEST", DisplayName: "Test", Icon: "test"})
	m := NewManager(map[string]Credentials{"test": {ClientID: "id"}}, nil, nil)

	if !m.IsAvailable("test") {
		t.Fatal("presence-only provider should be available immediately")
	}
	avail := m.Available()
	if len(avail) != 1 || avail[0].Name != "test" || avail[0].DisplayName != "Test" {
		t.Fatalf("Available() = %+v", avail)
	}
}

func TestAvailabilityDiscovery(t *testing.T) {
	var healthy bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" && healthy {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":         "http://issuer",
				"token_endpoint": "http://issuer/token",
			})
			return
		}
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	withTestProvider(t, ProviderDef{Name: "test", EnvPrefix: "OAUTH_TEST", Issuer: srv.URL})
	m := NewManager(map[string]Credentials{"test": {ClientID: "id"}}, srv.Client(), nil)

	// Issuer-backed providers start unavailable until validated.
	if m.IsAvailable("test") {
		t.Fatal("issuer provider should start unavailable")
	}
	healthy = true
	m.RefreshAvailability(context.Background())
	if !m.IsAvailable("test") {
		t.Fatal("provider should be available after successful discovery")
	}
	healthy = false
	m.RefreshAvailability(context.Background())
	if m.IsAvailable("test") {
		t.Fatal("provider should be withheld after discovery failure")
	}
}

func TestExchangeMapsUserInfo(t *testing.T) {
	var gotVerifier string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotVerifier = r.Form.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "token_type": "Bearer"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			http.Error(w, "no bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// numeric id exercises coerceString
		_, _ = w.Write([]byte(`{"uid":12345,"mail":"a@b.com","full":"Ada L","pic":"http://img/1"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	withTestProvider(t, ProviderDef{
		Name: "test", EnvPrefix: "OAUTH_TEST",
		AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", UserInfoURL: srv.URL + "/userinfo",
		Fields: FieldMap{ID: "uid", Email: "mail", Name: "full", Picture: "pic"},
	})
	m := NewManager(map[string]Credentials{"test": {ClientID: "id", ClientSecret: "sec"}}, srv.Client(), nil)

	verifier := GenerateVerifier()
	info, err := m.Exchange(context.Background(), "test", "the-code", "http://127.0.0.1:9/cb", verifier)
	if err != nil {
		t.Fatal(err)
	}
	if gotVerifier != verifier {
		t.Errorf("token endpoint got verifier %q, want %q", gotVerifier, verifier)
	}
	if info.ID != "12345" || info.Email != "a@b.com" || info.Name != "Ada L" || info.Picture != "http://img/1" {
		t.Errorf("mapped info = %+v", info)
	}
}

func TestParseUserInfoPlaceholderEmailAndMissingID(t *testing.T) {
	def := ProviderDef{Name: "test", Fields: FieldMap{ID: "id", Email: "email", Name: "name"}}

	info, err := parseUserInfo(def, []byte(`{"id":"abc","name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.Email, "test_abc@") {
		t.Errorf("placeholder email = %q", info.Email)
	}

	if _, err := parseUserInfo(def, []byte(`{"name":"x"}`)); err == nil {
		t.Error("expected error when id field is missing")
	}
}

func TestCoerceString(t *testing.T) {
	cases := map[string]any{
		"str":  "str",
		"7":    float64(7),
		"7.5":  float64(7.5),
		"true": true,
		"":     nil,
	}
	for want, in := range cases {
		if got := coerceString(in); got != want {
			t.Errorf("coerceString(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDefinitionAndEnvKeys(t *testing.T) {
	d, ok := Definition("google")
	if !ok {
		t.Fatal("google must be in the registry")
	}
	id, secret, redirect := d.EnvKeys()
	if id != "OAUTH_GOOGLE_CLIENT_ID" || secret != "OAUTH_GOOGLE_CLIENT_SECRET" || redirect != "OAUTH_GOOGLE_REDIRECT_URL" {
		t.Errorf("env keys = %q %q %q", id, secret, redirect)
	}
	if _, ok := Definition("nope"); ok {
		t.Error("unknown provider should not resolve")
	}
}
