package auth_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/auth"
	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/infra/migrations"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) *db.DB {
	cfg := config.DatabaseConfig{
		Host:            "localhost",
		Port:            5432,
		User:            "postgres",
		Password:        "postgres",
		Database:        "concord_test",
		MaxConns:        5,
		MinConns:        1,
		MaxConnLifetime: 5 * time.Minute,
		MaxConnIdleTime: 5 * time.Minute,
	}

	database, err := db.New(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = migrations.Run(ctx, database.Pool)
	require.NoError(t, err, "Failed to run migrations")

	cleanupTestData(t, database)

	return database
}

func cleanupTestData(t *testing.T, database *db.DB) {
	ctx := context.Background()

	tables := []string{
		"refresh_tokens",
		"messages",
		"memberships",
		"room_bans",
		"room_mutes",
		"rooms",
		"voice_servers",
		"users",
	}

	for _, table := range tables {
		query := fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table)
		_, err := database.Pool.Exec(ctx, query)
		if err != nil {
			t.Logf("Warning: failed to truncate table %s: %v", table, err)
		}
	}
}

func uniqueHandle(base string) string {
	return fmt.Sprintf("%s_%s", base, uuid.New().String()[:8])
}

func TestRegister(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

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
		{
			name:        "valid registration",
			handle:      uniqueHandle("testuser"),
			password:    "password123",
			displayName: "Test User",
			wantErr:     false,
		},
		{
			name:        "short handle",
			handle:      "ab",
			password:    "password123",
			displayName: "Test",
			wantErr:     true,
		},
		{
			name:        "short password",
			handle:      uniqueHandle("testuser2"),
			password:    "12345",
			displayName: "Test",
			wantErr:     true,
		},
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
	database := setupTestDB(t)
	defer database.Close()

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
		{
			name:     "valid login",
			handle:   testHandle,
			password: "password123",
			wantErr:  false,
		},
		{
			name:     "wrong password",
			handle:   testHandle,
			password: "wrongpassword",
			wantErr:  true,
		},
		{
			name:     "nonexistent user",
			handle:   uniqueHandle("doesnotexist"),
			password: "password123",
			wantErr:  true,
		},
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
	database := setupTestDB(t)
	defer database.Close()

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
