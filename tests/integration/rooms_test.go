package integration_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/infra/migrations"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type contextKey string

const userIDKey contextKey = "user_id"

func setupTestDB(t *testing.T) *db.DB {
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

	ctx := context.Background()
	err = migrations.Run(ctx, database.Pool)
	require.NoError(t, err)

	cleanupTestData(t, database)

	return database
}

func cleanupTestData(t *testing.T, database *db.DB) {
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

func uniqueHandle(base string) string {
	return fmt.Sprintf("%s_%s", base, uuid.New().String()[:8])
}

func TestRoomCreationAndMembership(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	usersRepo := users.NewRepository(database.Pool)
	roomsRepo := rooms.NewRepository(database.Pool)
	ctx := context.Background()

	user := &users.User{
		ID:          uuid.New(),
		Handle:      uniqueHandle("roomtestuser"),
		DisplayName: "Room Test User",
	}
	err := usersRepo.Create(ctx, user)
	require.NoError(t, err)

	ctx = context.WithValue(ctx, userIDKey, user.ID.String())

	room := &rooms.Room{
		Name:      "Test Room",
		CreatedBy: user.ID,
	}
	err = roomsRepo.Create(ctx, room)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, room.ID)

	err = roomsRepo.AddMember(ctx, room.ID, user.ID, "admin")
	require.NoError(t, err)

	member, err := roomsRepo.GetMember(ctx, room.ID, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "admin", member.Role)

	members, err := roomsRepo.ListMembers(ctx, room.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)

	err = roomsRepo.UpdateMemberRole(ctx, room.ID, user.ID, "member")
	require.NoError(t, err)

	member, err = roomsRepo.GetMember(ctx, room.ID, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "member", member.Role)

	err = roomsRepo.RemoveMember(ctx, room.ID, user.ID)
	require.NoError(t, err)

	_, err = roomsRepo.GetMember(ctx, room.ID, user.ID)
	assert.Error(t, err)
}

func TestRoomsList(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	usersRepo := users.NewRepository(database.Pool)
	roomsRepo := rooms.NewRepository(database.Pool)
	ctx := context.Background()

	user := &users.User{
		ID:          uuid.New(),
		Handle:      uniqueHandle("listroomsuser"),
		DisplayName: "List Rooms User",
	}
	err := usersRepo.Create(ctx, user)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		room := &rooms.Room{
			Name:      fmt.Sprintf("Test Room %d", i),
			CreatedBy: user.ID,
		}
		err := roomsRepo.Create(ctx, room)
		require.NoError(t, err)

		err = roomsRepo.AddMember(ctx, room.ID, user.ID, "admin")
		require.NoError(t, err)
	}

	userRooms, err := roomsRepo.ListForUser(ctx, user.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(userRooms), 3)
}
