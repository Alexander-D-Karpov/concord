package integration_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/common/logging"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/tests/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoomCreationAndMembership(t *testing.T) {
	database := testutil.GetDB(t)
	aside := testutil.GetAside(t)

	logger, _ := logging.Init("info", "json", "stdout", false, "")
	eventsHub := events.NewHub(logger, database.Pool, aside)

	usersRepo := users.NewRepository(database.Pool)
	roomsRepo := rooms.NewRepository(database.Pool)
	ctx := context.Background()

	user := &users.User{
		ID:          uuid.New(),
		Handle:      uniqueHandle("roomtestuser"),
		DisplayName: "Room Test User",
	}
	require.NoError(t, usersRepo.Create(ctx, user))

	room := &rooms.Room{
		Name:      "Test Room",
		CreatedBy: user.ID,
	}
	require.NoError(t, roomsRepo.Create(ctx, room))
	assert.NotEqual(t, uuid.Nil, room.ID)

	require.NoError(t, roomsRepo.AddMember(ctx, room.ID, user.ID, "admin"))
	if eventsHub != nil {
		eventsHub.NotifyRoomJoin(user.ID.String(), room.ID.String())
	}

	member, err := roomsRepo.GetMember(ctx, room.ID, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "admin", member.Role)

	members, err := roomsRepo.ListMembers(ctx, room.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)

	require.NoError(t, roomsRepo.UpdateMemberRole(ctx, room.ID, user.ID, "member"))

	member, err = roomsRepo.GetMember(ctx, room.ID, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "member", member.Role)

	require.NoError(t, roomsRepo.RemoveMember(ctx, room.ID, user.ID))
	if eventsHub != nil {
		eventsHub.NotifyRoomLeave(user.ID.String(), room.ID.String())
	}

	_, err = roomsRepo.GetMember(ctx, room.ID, user.ID)
	assert.Error(t, err)
}

func TestRoomsList(t *testing.T) {
	database := testutil.GetDB(t)
	aside := testutil.GetAside(t)

	logger, _ := logging.Init("info", "json", "stdout", false, "")
	eventsHub := events.NewHub(logger, database.Pool, aside)

	usersRepo := users.NewRepository(database.Pool)
	roomsRepo := rooms.NewRepository(database.Pool)
	ctx := context.Background()

	user := &users.User{
		ID:          uuid.New(),
		Handle:      uniqueHandle("listroomsuser"),
		DisplayName: "List Rooms User",
	}
	require.NoError(t, usersRepo.Create(ctx, user))

	for i := 0; i < 3; i++ {
		room := &rooms.Room{
			Name:      fmt.Sprintf("Test Room %d", i),
			CreatedBy: user.ID,
		}
		require.NoError(t, roomsRepo.Create(ctx, room))
		require.NoError(t, roomsRepo.AddMember(ctx, room.ID, user.ID, "admin"))
		if eventsHub != nil {
			eventsHub.NotifyRoomJoin(user.ID.String(), room.ID.String())
		}
	}

	userRooms, err := roomsRepo.ListForUser(ctx, user.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(userRooms), 3)
}
