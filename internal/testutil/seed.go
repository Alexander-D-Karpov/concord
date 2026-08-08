package testutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedUser inserts a user with the given handle (also used as the display name) and
// returns its generated ID, failing the test on error.
func SeedUser(t *testing.T, pool *pgxpool.Pool, handle string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO users (handle, display_name) VALUES ($1, $2) RETURNING id`,
		handle, handle,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed user %q: %v", handle, err)
	}
	return id
}

// SeedRoom inserts a room owned by owner with the given slow-mode interval (seconds),
// generating a random unique name, and returns its ID. Fails the test on error.
func SeedRoom(t *testing.T, pool *pgxpool.Pool, owner uuid.UUID, slowModeInterval int) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO rooms (name, created_by, slow_mode_interval) VALUES ($1, $2, $3) RETURNING id`,
		fmt.Sprintf("room-%s", uuid.NewString()[:8]), owner, slowModeInterval,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed room: %v", err)
	}
	return id
}

// SeedMembership inserts a membership row joining userID to roomID with the given role
// (e.g. "owner", "member"), failing the test on error.
func SeedMembership(t *testing.T, pool *pgxpool.Pool, roomID, userID uuid.UUID, role string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (room_id, user_id, role) VALUES ($1, $2, $3)`,
		roomID, userID, role,
	)
	if err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// SeedDMChannel inserts a direct-message channel between users a and b and returns its
// ID. The two IDs are stored in a canonical (sorted) order so the pair maps to a single
// channel regardless of argument order. Fails the test on error.
func SeedDMChannel(t *testing.T, pool *pgxpool.Pool, a, b uuid.UUID) uuid.UUID {
	t.Helper()
	u1, u2 := a, b
	if u1.String() > u2.String() {
		u1, u2 = u2, u1
	}
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO dm_channels (user1_id, user2_id) VALUES ($1, $2) RETURNING id`,
		u1, u2,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed dm channel: %v", err)
	}
	return id
}
