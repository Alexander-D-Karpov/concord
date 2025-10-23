package integration_test

import (
	"context"
	"testing"
	"time"

	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAPITestDB(t *testing.T) *db.DB {
	cfg := config.DatabaseConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "postgres",
		Password: "postgres",
		Database: "concord_test",
		MaxConns: 5,
		MinConns: 1,
	}

	database, err := db.New(cfg)
	require.NoError(t, err)

	cleanupAPITestData(t, database)

	return database
}

func cleanupAPITestData(t *testing.T, database *db.DB) {
	ctx := context.Background()

	_, err := database.Pool.Exec(ctx, "DELETE FROM refresh_tokens")
	require.NoError(t, err)

	_, err = database.Pool.Exec(ctx, "DELETE FROM messages")
	require.NoError(t, err)

	_, err = database.Pool.Exec(ctx, "DELETE FROM memberships")
	require.NoError(t, err)

	_, err = database.Pool.Exec(ctx, "DELETE FROM rooms")
	require.NoError(t, err)

	_, err = database.Pool.Exec(ctx, "DELETE FROM users")
	require.NoError(t, err)
}

func TestAuthAPI(t *testing.T) {
	database := setupAPITestDB(t)
	defer database.Close()

	usersRepo := users.NewRepository(database.Pool)
	jwtManager := jwt.NewManager("test-secret", "test-voice-secret")
	authCfg := config.AuthConfig{
		JWTExpiration:     15 * time.Minute,
		RefreshExpiration: 7 * 24 * time.Hour,
	}

	service := auth.NewService(usersRepo, database.Pool, jwtManager, nil, nil, authCfg)
	handler := auth.NewHandler(service)

	ctx := context.Background()

	t.Run("Register", func(t *testing.T) {
		req := &authv1.RegisterRequest{
			Handle:      uniqueHandle("apitest"),
			Password:    "password123",
			DisplayName: "API Test User",
		}

		token, err := handler.Register(ctx, req)
		require.NoError(t, err)
		assert.NotEmpty(t, token.AccessToken)
		assert.NotEmpty(t, token.RefreshToken)
	})

	t.Run("Login", func(t *testing.T) {
		testHandle := uniqueHandle("apilogin")

		regReq := &authv1.RegisterRequest{
			Handle:      testHandle,
			Password:    "password123",
			DisplayName: "API Login User",
		}
		_, err := handler.Register(ctx, regReq)
		require.NoError(t, err)

		req := &authv1.LoginPasswordRequest{
			Handle:   testHandle,
			Password: "password123",
		}

		token, err := handler.LoginPassword(ctx, req)
		require.NoError(t, err)
		assert.NotEmpty(t, token.AccessToken)
	})
}

func TestRoomsAPI(t *testing.T) {
	database := setupAPITestDB(t)
	defer database.Close()

	usersRepo := users.NewRepository(database.Pool)
	roomsRepo := rooms.NewRepository(database.Pool)

	user := &users.User{
		ID:          uuid.New(),
		Handle:      uniqueHandle("roomapitest"),
		DisplayName: "Room API Test",
	}
	err := usersRepo.Create(context.Background(), user)
	require.NoError(t, err)

	roomsService := rooms.NewService(roomsRepo)
	handler := rooms.NewHandler(roomsService)

	ctx := interceptor.ContextWithAuth(context.Background(), user.ID.String(), user.Handle, nil)

	t.Run("CreateRoom", func(t *testing.T) {
		req := &roomsv1.CreateRoomRequest{
			Name: "API Test Room",
		}

		room, err := handler.CreateRoom(ctx, req)
		require.NoError(t, err)
		assert.NotEmpty(t, room.Id)
		assert.Equal(t, "API Test Room", room.Name)
	})

	t.Run("ListRooms", func(t *testing.T) {
		req := &roomsv1.CreateRoomRequest{
			Name: "List Test Room",
		}
		_, err := handler.CreateRoom(ctx, req)
		require.NoError(t, err)

		listReq := &roomsv1.ListRoomsForUserRequest{}
		resp, err := handler.ListRoomsForUser(ctx, listReq)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(resp.Rooms), 1)
	})
}
