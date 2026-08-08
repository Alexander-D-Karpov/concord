package auth

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	apperr "github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func newAuthTestCache(t *testing.T) *cache.Cache {
	t.Helper()
	host := os.Getenv("REDIS_HOST")
	if host == "" {
		host = "localhost"
	}
	port := 6379
	if p := os.Getenv("REDIS_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	c, err := cache.New(host, port, os.Getenv("REDIS_PASSWORD"), 0)
	if err != nil {
		t.Skipf("redis unavailable, skipping: %v", err)
	}
	return c
}

// TestLoginPasswordLockout proves that repeated failed password logins lock the
// account: after maxAttempts failures, even a subsequent login with the CORRECT
// password is rejected as rate-limited (not authenticated) until the lock expires.
func TestLoginPasswordLockout(t *testing.T) {
	pool := testutil.Pool(t)
	c := newAuthTestCache(t)
	ctx := context.Background()

	const correctPassword = "correct-horse-battery"
	hash, err := bcrypt.GenerateFromPassword([]byte(correctPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	handle := "lockme-" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (handle, display_name, password_hash) VALUES ($1, $1, $2)`,
		handle, string(hash),
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Delete(ctx, "login_attempts:"+handle)
		_ = c.Delete(ctx, "account_locked:"+handle)
	})

	cfg := config.AuthConfig{
		JWTSecret:          "test-secret",
		JWTExpiration:      time.Hour,
		RefreshExpiration:  24 * time.Hour,
		LoginMaxAttempts:   3,
		LoginLockoutPeriod: time.Minute,
		LoginAttemptWindow: time.Minute,
	}
	svc := NewService(users.NewRepository(pool), pool, jwt.NewManager("test-secret", "voice-secret"), nil, c, cfg)

	// Three wrong-password attempts, each an ordinary auth failure.
	for i := 0; i < 3; i++ {
		if _, err := svc.LoginPassword(ctx, handle, "wrong-password"); err == nil {
			t.Fatalf("attempt %d: expected failure for wrong password", i+1)
		}
	}

	// The account is now locked: the correct password must be rejected as
	// rate-limited, proving the lockout is actually enforced.
	tokens, err := svc.LoginPassword(ctx, handle, correctPassword)
	if err == nil {
		t.Fatal("expected lockout to reject the correct password, but login succeeded")
	}
	if tokens != nil {
		t.Fatal("expected nil tokens when locked out")
	}
	if !apperr.IsTooManyRequests(err) {
		t.Errorf("expected a too-many-requests error, got %v", err)
	}
}
