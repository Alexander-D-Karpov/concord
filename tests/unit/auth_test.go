package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth"
	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/tests/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func uniqueHandle(base string) string {
	return base + "_" + uuid.New().String()[:8]
}

func TestRegister(t *testing.T) {
	database := testutil.GetDB(t)

	usersRepo := users.NewRepository(database.Pool)
	jwtManager := jwt.NewManager("test-secret", "test-voice-secret")
	authCfg := config.AuthConfig{
		JWTExpiration:     15 * time.Minute,
		RefreshExpiration: 7 * 24 * time.Hour,
	}

	service := auth.NewService(usersRepo, database.Pool, jwtManager, nil, nil, authCfg)

	tests := []struct {
		name        string
		handle      string
		password    string
		displayName string
		wantErr     bool
	}{
		{"valid registration", uniqueHandle("testuser"), "password123", "Test User", false},
		{"short handle", "ab", "password123", "Test", true},
		{"short password", uniqueHandle("testuser2"), "12345", "Test", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := service.Register(context.Background(), tt.handle, tt.password, tt.displayName)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.NotEmpty(t, tokens.AccessToken)
			assert.NotEmpty(t, tokens.RefreshToken)
			assert.Greater(t, tokens.ExpiresIn, int64(0))
		})
	}
}

func TestLoginPassword(t *testing.T) {
	database := testutil.GetDB(t)

	usersRepo := users.NewRepository(database.Pool)
	jwtManager := jwt.NewManager("test-secret", "test-voice-secret")
	authCfg := config.AuthConfig{
		JWTExpiration:     15 * time.Minute,
		RefreshExpiration: 7 * 24 * time.Hour,
	}

	service := auth.NewService(usersRepo, database.Pool, jwtManager, nil, nil, authCfg)
	ctx := context.Background()

	testHandle := uniqueHandle("logintest")
	_, err := service.Register(ctx, testHandle, "password123", "Login Test")
	require.NoError(t, err)

	tests := []struct {
		name     string
		handle   string
		password string
		wantErr  bool
	}{
		{"valid login", testHandle, "password123", false},
		{"wrong password", testHandle, "wrongpassword", true},
		{"nonexistent user", uniqueHandle("doesnotexist"), "password123", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := service.LoginPassword(ctx, tt.handle, tt.password)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.NotEmpty(t, tokens.AccessToken)
			assert.NotEmpty(t, tokens.RefreshToken)
		})
	}
}

func TestRefreshToken(t *testing.T) {
	database := testutil.GetDB(t)

	usersRepo := users.NewRepository(database.Pool)
	jwtManager := jwt.NewManager("test-secret", "test-voice-secret")
	authCfg := config.AuthConfig{
		JWTExpiration:     15 * time.Minute,
		RefreshExpiration: 7 * 24 * time.Hour,
	}

	service := auth.NewService(usersRepo, database.Pool, jwtManager, nil, nil, authCfg)
	ctx := context.Background()

	testHandle := uniqueHandle("refreshtest")
	initialTokens, err := service.Register(ctx, testHandle, "password123", "Refresh Test")
	require.NoError(t, err)

	newTokens, err := service.RefreshToken(ctx, initialTokens.RefreshToken)
	require.NoError(t, err)
	assert.NotEmpty(t, newTokens.AccessToken)
	assert.NotEmpty(t, newTokens.RefreshToken)
	assert.NotEqual(t, initialTokens.AccessToken, newTokens.AccessToken)

	_, err = service.RefreshToken(ctx, "invalid-token")
	assert.Error(t, err)
}
