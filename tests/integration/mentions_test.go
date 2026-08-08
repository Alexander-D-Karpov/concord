package integration

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/messaging/mentions"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

func TestMentionParserResolvesHandles(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.Truncate(t, pool, "users")

	alice := testutil.SeedUser(t, pool, "alice")
	bob := testutil.SeedUser(t, pool, "bob")

	parser := mentions.NewParser(pool)
	ctx := context.Background()

	got, err := parser.Parse(ctx, "hey @alice and @bob", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	assertContains(t, got, alice)
	assertContains(t, got, bob)
	if len(got) != 2 {
		t.Fatalf("want 2 mentions, got %d: %v", len(got), got)
	}
}

func TestMentionParserMergesHints(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.Truncate(t, pool, "users")

	alice := testutil.SeedUser(t, pool, "alice")
	bob := testutil.SeedUser(t, pool, "bob")

	parser := mentions.NewParser(pool)

	got, err := parser.Parse(context.Background(), "ping @alice", []uuid.UUID{bob})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	assertContains(t, got, alice)
	assertContains(t, got, bob)
	if len(got) != 2 {
		t.Fatalf("want 2 merged mentions, got %d: %v", len(got), got)
	}
}

func TestMentionParserDedupes(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.Truncate(t, pool, "users")

	alice := testutil.SeedUser(t, pool, "alice")

	parser := mentions.NewParser(pool)

	got, err := parser.Parse(context.Background(), "@alice @alice", []uuid.UUID{alice})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0] != alice {
		t.Fatalf("want [alice], got %v", got)
	}
}

func TestMentionParserDropsUnknownHandles(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.Truncate(t, pool, "users")

	alice := testutil.SeedUser(t, pool, "alice")

	parser := mentions.NewParser(pool)

	got, err := parser.Parse(context.Background(), "@alice @ghost", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0] != alice {
		t.Fatalf("want only alice, got %v", got)
	}
}

func assertContains(t *testing.T, ids []uuid.UUID, want uuid.UUID) {
	t.Helper()
	for _, id := range ids {
		if id == want {
			return
		}
	}
	t.Fatalf("expected %s in %v", want, ids)
}
