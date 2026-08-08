package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/infra/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Package-level state backs the single per-process test database: once guards its
// one-time creation and migration, shared holds the connected pool, dbName is the
// randomly named database currently in use, and teardownMu/tornDown make Teardown
// safe to call more than once.
var (
	once       sync.Once
	shared     *db.DB
	dbName     string
	teardownMu sync.Mutex
	tornDown   bool
)

// envOr returns the value of environment variable key, or def when it is unset or empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// randomSuffix returns a 6-character hex string used to give each test run a unique
// database name, so parallel or repeated runs never collide on the same database.
func randomSuffix() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// baseConfig builds the DatabaseConfig shared by the admin and application
// connections from the DB_HOST/DB_USER/DB_PASSWORD env vars (defaulting to a local
// postgres). The Database field is intentionally left empty for the caller to set.
func baseConfig() config.DatabaseConfig {
	return config.DatabaseConfig{
		Host:            envOr("DB_HOST", "localhost"),
		Port:            5432,
		User:            envOr("DB_USER", "postgres"),
		Password:        envOr("DB_PASSWORD", "postgres"),
		Database:        "", // set below
		MaxConns:        10,
		MinConns:        2,
		MaxConnLifetime: 5 * time.Minute,
		MaxConnIdleTime: 5 * time.Minute,
	}
}

// GetDB creates a UNIQUE per-process DB, runs migrations once, and returns a shared pool.
func GetDB(t *testing.T) *db.DB {
	t.Helper()

	once.Do(func() {
		base := envOr("DB_NAME", "concord_test")
		dbName = fmt.Sprintf("%s_%s", base, randomSuffix())

		adminCfg := baseConfig()
		adminCfg.Database = "postgres"

		adminDB, err := db.New(adminCfg)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		_, err = adminDB.Pool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, dbName))
		require.NoError(t, err, "failed to create test database %q", dbName)
		adminDB.Close()

		appCfg := baseConfig()
		appCfg.Database = dbName
		shared, err = db.New(appCfg)
		require.NoError(t, err)

		err = migrations.Run(ctx, shared.Pool)
		require.NoError(t, err, "Failed to run migrations in %s", dbName)
	})

	return shared
}

// CurrentDBName returns the randomly generated name of the active test database, or
// an empty string before GetDB has created one.
func CurrentDBName() string { return dbName }

// Pool returns the pgx connection pool of the shared test database, creating and
// migrating it on first use via GetDB.
func Pool(t *testing.T) *pgxpool.Pool {
	return GetDB(t).Pool
}

// Teardown closes the shared pool and drops the randomly named test database. It is
// idempotent (guarded so a repeated call is a no-op) and typically runs once from a
// TestMain after all tests finish. It uses DROP DATABASE ... WITH (FORCE) to evict any
// lingering connections, and does nothing if no database was ever created.
func Teardown() {
	teardownMu.Lock()
	defer teardownMu.Unlock()
	if tornDown {
		return
	}
	tornDown = true

	if shared != nil {
		shared.Close()
	}

	if dbName == "" {
		return
	}

	adminCfg := baseConfig()
	adminCfg.Database = "postgres"
	adminDB, err := db.New(adminCfg)
	if err != nil {
		return
	}
	defer adminDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// FORCE handles any lingering connections.
	_, _ = adminDB.Pool.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, dbName))
}
