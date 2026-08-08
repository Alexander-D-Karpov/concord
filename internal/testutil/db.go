package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Each test process gets its own freshly-created, migrated database so that
// packages running in parallel (as `go test ./...` does) never race on shared
// tables — in particular, one package's TRUNCATE cannot wipe rows another package
// is using. The isolated database is set up once per process.
var (
	setupOnce  sync.Once
	sharedPool *pgxpool.Pool
	setupErr   error
	testDBName string
)

// baseName is the database used both as the connection target default and as the
// maintenance connection for creating the per-process test database.
func baseName() string { return env("TEST_DB_NAME", env("DB_NAME", "concord_test")) }

// adminDSN builds a connection string to the given database from DB_* env vars.
func adminDSN(dbName string) string {
	host := env("DB_HOST", "localhost")
	port := env("DB_PORT", "5432")
	user := env("DB_USER", "postgres")
	pass := env("DB_PASSWORD", "postgres")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port, dbName)
}

// env returns the value of environment variable k, or def when it is unset or empty.
func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// CurrentDBName returns the name of this process's isolated test database (empty
// until Pool has been called).
func CurrentDBName() string { return testDBName }

// setupIsolatedDB creates a uniquely-named database for this process, connects to
// it, and runs migrations. The connection used to CREATE DATABASE targets the base
// database (default concord_test), which the test harness ensures exists.
func setupIsolatedDB(ctx context.Context) (*pgxpool.Pool, error) {
	testDBName = fmt.Sprintf("concord_test_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)

	admin, err := pgxpool.New(ctx, adminDSN(baseName()))
	if err != nil {
		return nil, err
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		return nil, err
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+testDBName); err != nil {
		return nil, err
	}

	pool, err := pgxpool.New(ctx, adminDSN(testDBName))
	if err != nil {
		return nil, err
	}
	if err := migrations.Run(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// Pool returns a ready pgx pool for this process's isolated, migrated test
// database, creating it on first use. It skips the test (not fails) when Postgres
// is unavailable, so suites degrade gracefully without a database, but fails the
// test if database creation or migrations error.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	setupOnce.Do(func() {
		sharedPool, setupErr = setupIsolatedDB(ctx)
	})
	if setupErr != nil {
		if isUnavailable(setupErr) {
			t.Skipf("test database unavailable: %v", setupErr)
		}
		t.Fatalf("test database setup failed: %v", setupErr)
	}
	return sharedPool
}

// isUnavailable reports whether err looks like the database being unreachable
// (versus a real setup error), so Pool can skip rather than fail.
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connect") || strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") || strings.Contains(msg, "dial")
}

// Truncate empties the named tables with RESTART IDENTITY CASCADE (resetting serial
// sequences and cascading to dependents), typically to isolate tests within a
// package. It is a no-op when no tables are given and fails the test on any SQL
// error. Because each process has its own database, this never affects other
// packages.
func Truncate(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		return
	}
	ctx := context.Background()
	stmt := "TRUNCATE " + tables[0]
	for _, tbl := range tables[1:] {
		stmt += ", " + tbl
	}
	stmt += " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("truncate failed: %v", err)
	}
}
