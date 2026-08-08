package config

import (
	"os"
	"testing"
)

func TestPushConfigDefaults(t *testing.T) {
	os.Unsetenv("PUSH_ENABLED")
	os.Unsetenv("PUSH_CREDENTIALS_FILE")
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
