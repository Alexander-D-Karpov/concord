package auth

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/auth/oauth"
	apperr "github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// oauthTestManager builds a Manager with a presence-available test provider so
// BeginOAuth succeeds without any network. The failure-path assertions below never
// reach the code exchange, so the provider's endpoints are irrelevant.
func oauthTestManager(t *testing.T) *oauth.Manager {
	t.Helper()
	orig := oauth.Registry
	oauth.Registry = append(append([]oauth.ProviderDef(nil), orig...), oauth.ProviderDef{
		Name: "tprov", EnvPrefix: "OAUTH_TPROV", DisplayName: "TProv", Icon: "tp",
		AuthURL: "https://prov.example/auth", TokenURL: "https://prov.example/token",
		UserInfoURL: "https://prov.example/userinfo",
		Fields:      oauth.FieldMap{ID: "id", Email: "email", Name: "name"},
	})
	t.Cleanup(func() { oauth.Registry = orig })

	return oauth.NewManager(map[string]oauth.Credentials{
		"tprov": {ClientID: "id", ClientSecret: "sec", RedirectURLs: []string{"https://app.example/cb"}},
	}, nil, nil)
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", want)
	}
	if got := status.Code(apperr.ToGRPCError(err)); got != want {
		t.Fatalf("error code = %v, want %v (err=%v)", got, want, err)
	}
}

// TestOAuthStateLifecycle exercises the server-side state machine end to end
// against Redis: BeginOAuth persists the state, a mismatched CompleteOAuth is
// rejected AND consumes the state (one-time), and the now-consumed state can no
// longer be replayed. Requires Redis; skips otherwise.
func TestOAuthStateLifecycle(t *testing.T) {
	c := newAuthTestCache(t)
	svc := &Service{oauth: oauthTestManager(t), cache: c}
	ctx := context.Background()
	redirect := "http://127.0.0.1:5555/cb" // loopback, allowed by rule

	authURL, state, err := svc.BeginOAuth(ctx, "tprov", redirect)
	if err != nil {
		t.Fatalf("BeginOAuth: %v", err)
	}
	if authURL == "" || state == "" {
		t.Fatalf("empty begin result: url=%q state=%q", authURL, state)
	}
	if ok, _ := c.Exists(ctx, oauthStateKey(state)); !ok {
		t.Fatal("state should be stored after BeginOAuth")
	}

	// Provider mismatch is rejected — and consumes the state before returning.
	_, err = svc.CompleteOAuth(ctx, "other", "code", state, redirect)
	wantCode(t, err, codes.Unauthenticated)
	if ok, _ := c.Exists(ctx, oauthStateKey(state)); ok {
		t.Fatal("state must be consumed (deleted) even on a failed CompleteOAuth")
	}

	// The consumed state cannot be replayed, even with the correct provider.
	_, err = svc.CompleteOAuth(ctx, "tprov", "code", state, redirect)
	wantCode(t, err, codes.Unauthenticated)

	// An unknown state is rejected.
	_, err = svc.CompleteOAuth(ctx, "tprov", "code", "never-issued", redirect)
	wantCode(t, err, codes.Unauthenticated)

	// Empty state is rejected.
	_, err = svc.CompleteOAuth(ctx, "tprov", "code", "", redirect)
	wantCode(t, err, codes.Unauthenticated)
}

// TestOAuthRedirectMismatchRejected proves a CompleteOAuth whose redirect URI does
// not match the one bound at BeginOAuth is rejected. Requires Redis; skips otherwise.
func TestOAuthRedirectMismatchRejected(t *testing.T) {
	c := newAuthTestCache(t)
	svc := &Service{oauth: oauthTestManager(t), cache: c}
	ctx := context.Background()

	_, state, err := svc.BeginOAuth(ctx, "tprov", "http://127.0.0.1:5555/cb")
	if err != nil {
		t.Fatalf("BeginOAuth: %v", err)
	}
	_, err = svc.CompleteOAuth(ctx, "tprov", "code", state, "http://127.0.0.1:9999/other")
	wantCode(t, err, codes.Unauthenticated)
}

// TestBeginOAuthRejectsBadRedirect proves a non-loopback redirect not on the
// allowlist is rejected before any state is stored.
func TestBeginOAuthRejectsBadRedirect(t *testing.T) {
	c := newAuthTestCache(t)
	svc := &Service{oauth: oauthTestManager(t), cache: c}

	_, _, err := svc.BeginOAuth(context.Background(), "tprov", "https://evil.example/cb")
	wantCode(t, err, codes.InvalidArgument)
}

// TestBeginOAuthUnavailableProvider proves an unconfigured provider name is rejected.
func TestBeginOAuthUnavailableProvider(t *testing.T) {
	c := newAuthTestCache(t)
	svc := &Service{oauth: oauthTestManager(t), cache: c}

	_, _, err := svc.BeginOAuth(context.Background(), "nonexistent", "http://127.0.0.1:1/cb")
	wantCode(t, err, codes.InvalidArgument)
}
