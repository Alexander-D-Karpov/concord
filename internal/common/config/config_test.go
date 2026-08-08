package config

import (
	"os"
	"testing"
)

func TestPushConfigDefaults(t *testing.T) {
	_ = os.Unsetenv("PUSH_ENABLED")
	_ = os.Unsetenv("PUSH_CREDENTIALS_FILE")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Push.Enabled {
		t.Error("expected push disabled by default")
	}
	if cfg.Push.CredentialsFile != "" {
		t.Errorf("expected empty credentials file by default, got %q", cfg.Push.CredentialsFile)
	}
}

func TestOAuthProvidersFromEnv(t *testing.T) {
	t.Setenv("OAUTH_GOOGLE_CLIENT_ID", "gid")
	t.Setenv("OAUTH_GOOGLE_CLIENT_SECRET", "gsec")
	t.Setenv("OAUTH_GOOGLE_REDIRECT_URL", "https://a/cb, https://b/cb ")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	g, ok := cfg.Auth.OAuth["google"]
	if !ok {
		t.Fatal("google should be configured when its client id is set")
	}
	if g.ClientID != "gid" || g.ClientSecret != "gsec" {
		t.Errorf("credentials = %+v", g)
	}
	if len(g.RedirectURLs) != 2 || g.RedirectURLs[0] != "https://a/cb" || g.RedirectURLs[1] != "https://b/cb" {
		t.Errorf("redirect allowlist = %v", g.RedirectURLs)
	}
}

func TestOAuthProvidersAbsentWhenUnset(t *testing.T) {
	t.Setenv("OAUTH_GOOGLE_CLIENT_ID", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Auth.OAuth["google"]; ok {
		t.Error("google should be absent when its client id is unset")
	}
}
