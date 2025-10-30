// tests/testutil/db.go
package testutil

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/infra/migrations"
	"github.com/stretchr/testify/require"
)

var (
	once           sync.Once
	sharedDB       *db.DB
	migrationsDone bool
	mu             sync.Mutex
)

// Stable advisory lock so only one package resets/runs migrations at a time.
const advisoryLockID int64 = 0x6A_636F_6E_63_6F_7264

func getConfig() config.DatabaseConfig {
	return config.DatabaseConfig{
		Host:            "localhost",
		Port:            5432,
		User:            "postgres",
		Password:        "postgres",
		Database:        "concord_test",
		MaxConns:        10,
		MinConns:        2,
		MaxConnLifetime: 5 * time.Minute,
		MaxConnIdleTime: 5 * time.Minute,
	}
}

func GetDB(t *testing.T) *db.DB {
	t.Helper()

	mu.Lock()
	defer mu.Unlock()

	once.Do(func() {
		var err error
		sharedDB, err = db.New(getConfig())
		require.NoError(t, err)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Serialize schema reset + migrations across packages.
	conn, err := sharedDB.Pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()

	_, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID)
	require.NoError(t, err)
	defer func() { _, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockID) }()

	if !migrationsDone {
		resetPublicSchema(t, ctx, sharedDB)

		err = migrations.Run(ctx, sharedDB.Pool)
		require.NoError(t, err, "Failed to run migrations")
		migrationsDone = true
	}

	truncateAll(t, sharedDB)

	return sharedDB
}

func resetPublicSchema(t *testing.T, ctx context.Context, database *db.DB) {
	t.Helper()

	_, _ = database.Pool.Exec(ctx, `DROP EXTENSION IF EXISTS "uuid-ossp"`)
	_, _ = database.Pool.Exec(ctx, `DROP EXTENSION IF EXISTS pgcrypto`)

	_, err := database.Pool.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE`)
	require.NoError(t, err)

	_, err = database.Pool.Exec(ctx, `CREATE SCHEMA public`)
	require.NoError(t, err)

	_, _ = database.Pool.Exec(ctx, `GRANT ALL ON SCHEMA public TO postgres`)
	_, _ = database.Pool.Exec(ctx, `GRANT ALL ON SCHEMA public TO public`)
}

func truncateAll(t *testing.T, database *db.DB) {
	t.Helper()
	ctx := context.Background()

	tables := []string{
		"message_reactions",
		"message_mentions",
		"message_attachments",
		"pinned_messages",
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
		q := fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table)
		if _, err := database.Pool.Exec(ctx, q); err != nil {
			t.Logf("Warning: failed to truncate table %s: %v", table, err)
		}
	}
}
