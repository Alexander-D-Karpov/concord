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
	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/tests/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func uniqueHandle(base string) string {
	return base + "_" + uuid.New().String()[:8]
}

func TestAuthAPI(t *testing.T) {
	database := testutil.GetDB(t)

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
		_, err := handler.Register(ctx, &authv1.RegisterRequest{
			Handle:      testHandle,
			Password:    "password123",
			DisplayName: "API Login User",
		})
		require.NoError(t, err)

		token, err := handler.LoginPassword(ctx, &authv1.LoginPasswordRequest{
			Handle:   testHandle,
			Password: "password123",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, token.AccessToken)
	})
}

func TestRoomsAPI(t *testing.T) {
	database := testutil.GetDB(t)
	aside := testutil.GetAside(t)

	logger, _ := logging.Init("info", "json", "stdout", false, "")
	eventsHub := events.NewHub(logger, database.Pool, aside)

	usersRepo := users.NewRepository(database.Pool)
	roomsRepo := rooms.NewRepository(database.Pool)

	user := &users.User{
		ID:          uuid.New(),
		Handle:      uniqueHandle("roomapitest"),
		DisplayName: "Room API Test",
	}
	require.NoError(t, usersRepo.Create(context.Background(), user))

	roomsService := rooms.NewService(roomsRepo, eventsHub, aside)
	handler := rooms.NewHandler(roomsService)

	ctx := interceptor.ContextWithAuth(context.Background(), user.ID.String(), user.Handle, nil)

	t.Run("CreateRoom", func(t *testing.T) {
		room, err := handler.CreateRoom(ctx, &roomsv1.CreateRoomRequest{Name: "API Test Room"})
		require.NoError(t, err)
		assert.NotEmpty(t, room.Id)
		assert.Equal(t, "API Test Room", room.Name)
	})

	t.Run("ListRooms", func(t *testing.T) {
		_, err := handler.CreateRoom(ctx, &roomsv1.CreateRoomRequest{Name: "List Test Room"})
		require.NoError(t, err)

		resp, err := handler.ListRoomsForUser(ctx, &roomsv1.ListRoomsForUserRequest{})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(resp.Rooms), 1)
	})
}
