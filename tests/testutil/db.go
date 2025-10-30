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

var (
	once       sync.Once
	shared     *db.DB
	dbName     string
	teardownMu sync.Mutex
	tornDown   bool
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func randomSuffix() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

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

func CurrentDBName() string { return dbName }

func Pool(t *testing.T) *pgxpool.Pool {
	return GetDB(t).Pool
}

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
