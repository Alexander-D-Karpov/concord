package integration

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/chat"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func seedRoomDirect(t *testing.T, pool interface {
	QueryRow(ctx context.Context, sql string, args ...any) interface{ Scan(...any) error }
}, owner uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO rooms (name, created_by) VALUES ($1, $2) RETURNING id`,
		"room-"+uuid.NewString()[:8], owner,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

func TestChatMessageCore_Characterization(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.Truncate(t, pool, "messages", "message_reactions", "pinned_messages", "memberships", "rooms", "users")

	ctx := context.Background()
	repo := chat.NewRepository(pool, infra.NewSnowflakeGenerator(1))

	author := testutil.SeedUser(t, pool, "author")
	var roomID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO rooms (name, created_by) VALUES ($1,$2) RETURNING id`,
		"r-"+uuid.NewString()[:8], author).Scan(&roomID))

	parent := &chat.Message{RoomID: roomID, AuthorID: author, Content: "parent", ReplyMentionAuthor: true}
	require.NoError(t, repo.Create(ctx, parent))

	reply := &chat.Message{RoomID: roomID, AuthorID: author, Content: "reply", ReplyToID: &parent.ID, ReplyMentionAuthor: true}
	require.NoError(t, repo.Create(ctx, reply))

	t.Run("reaction add/duplicate/remove", func(t *testing.T) {
		r1, err := repo.AddReaction(ctx, parent.ID, author, "👍")
		require.NoError(t, err)
		require.NotEqual(t, uuid.Nil, r1.ID)

		_, err = repo.AddReaction(ctx, parent.ID, author, "👍")
		require.Error(t, err) // Conflict: reaction already exists

		id, err := repo.RemoveReaction(ctx, parent.ID, author, "👍")
		require.NoError(t, err)
		require.NotEqual(t, uuid.Nil, id)

		_, err = repo.RemoveReaction(ctx, parent.ID, author, "👍")
		require.Error(t, err) // NotFound
	})

	t.Run("pin idempotent + unpin notfound", func(t *testing.T) {
		require.NoError(t, repo.PinMessage(ctx, roomID, parent.ID, author))
		require.NoError(t, repo.PinMessage(ctx, roomID, parent.ID, author)) // ON CONFLICT DO NOTHING

		pinned, err := repo.ListPinnedMessages(ctx, roomID)
		require.NoError(t, err)
		require.Len(t, pinned, 1)
		require.Equal(t, parent.ID, pinned[0].ID)
		require.Nil(t, pinned[0].Mentions) // room pinned: mentions NOT loaded (preserved)

		require.NoError(t, repo.UnpinMessage(ctx, roomID, parent.ID))
		require.Error(t, repo.UnpinMessage(ctx, roomID, parent.ID)) // NotFound
	})

	t.Run("thread replies ascending", func(t *testing.T) {
		replies, err := repo.GetThreadReplies(ctx, parent.ID, 50, 0)
		require.NoError(t, err)
		require.Len(t, replies, 1)
		require.Equal(t, reply.ID, replies[0].ID)
	})
}
