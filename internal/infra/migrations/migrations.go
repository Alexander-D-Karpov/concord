package migrations

import (
	"context"
	"embed"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrations embeds every .sql file in this directory at build time so the
// binary carries its schema migrations with no external files.
//
//go:embed *.sql
var migrations embed.FS

// MigrationStatus reports whether a single embedded migration has been applied.
type MigrationStatus struct {
	Version int
	Name    string
	Applied bool
}

// Status returns every embedded migration with whether it has been applied, in
// ascending version order. It ensures the tracking table exists but does not apply
// any migrations. Intended for `concord-cli migrate status`.
func Status(ctx context.Context, pool *pgxpool.Pool) ([]MigrationStatus, error) {
	if err := createMigrationsTable(ctx, pool); err != nil {
		return nil, err
	}
	applied, err := getAppliedVersions(ctx, pool)
	if err != nil {
		return nil, err
	}
	entries, err := migrations.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []MigrationStatus
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(e.Name(), "_", 2)
		if len(parts) != 2 {
			continue
		}
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		out = append(out, MigrationStatus{
			Version: v,
			Name:    strings.TrimSuffix(parts[1], ".sql"),
			Applied: applied[v],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Migration is one parsed SQL migration file. Version comes from the numeric
// filename prefix (NNN_name.sql) and orders application.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// migrationLockKey is the fixed Postgres advisory-lock key that serializes Run
// across processes. Its arbitrary value just needs to be stable and app-unique.
const migrationLockKey int64 = 0x636F6E636F7264 // "concord"

// Run applies all pending migrations in ascending version order. It ensures the
// schema_migrations bookkeeping table exists, skips versions already recorded
// there, and applies each remaining migration in its own transaction. It is
// idempotent: a fully migrated database is a no-op. Progress is logged via the
// standard log package.
//
// Run holds a session-level Postgres advisory lock for its duration, so concurrent
// callers (multiple API replicas starting together, or parallel test binaries
// sharing a database) serialize rather than racing on DDL and bookkeeping rows.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		// Best-effort release; the lock is also freed when the session ends.
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockKey)
	}()

	if err := createMigrationsTable(ctx, pool); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	appliedVersions, err := getAppliedVersions(ctx, pool)
	if err != nil {
		return fmt.Errorf("get applied versions: %w", err)
	}

	migrationsToApply, err := getMigrationsToApply(appliedVersions)
	if err != nil {
		return fmt.Errorf("get migrations to apply: %w", err)
	}

	log.Printf("Found %d migrations to apply", len(migrationsToApply))

	if len(migrationsToApply) == 0 {
		log.Printf("No migrations to apply. Applied versions: %v", appliedVersions)
		return nil
	}

	for _, migration := range migrationsToApply {
		log.Printf("Applying migration %d: %s", migration.Version, migration.Name)
		if err := applyMigration(ctx, pool, migration); err != nil {
			return fmt.Errorf("apply migration %d: %w", migration.Version, err)
		}
		log.Printf("Successfully applied migration %d", migration.Version)
	}

	return nil
}

// createMigrationsTable creates the schema_migrations tracking table if it does
// not already exist.
func createMigrationsTable(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
		)
	`)
	return err
}

// getAppliedVersions returns the set of migration versions already recorded in
// schema_migrations, keyed by version for O(1) lookup.
func getAppliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int]bool, error) {
	rows, err := pool.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	versions := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		versions[version] = true
	}

	return versions, rows.Err()
}

// getMigrationsToApply reads the embedded .sql files, parses each NNN_name.sql
// name into a Migration, drops any whose version is in appliedVersions, and
// returns the rest sorted ascending by version. Files with an unparseable name
// or version are logged and skipped, not treated as errors.
func getMigrationsToApply(appliedVersions map[int]bool) ([]Migration, error) {
	entries, err := migrations.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read migrations directory: %w", err)
	}

	log.Printf("Found %d files in migrations directory", len(entries))

	var toApply []Migration
	for _, entry := range entries {
		log.Printf("Processing file: %s (isDir: %v)", entry.Name(), entry.IsDir())

		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			log.Printf("Skipping file %s: invalid format (expected NNN_name.sql)", entry.Name())
			continue
		}

		version, err := strconv.Atoi(parts[0])
		if err != nil {
			log.Printf("Skipping file %s: invalid version number", entry.Name())
			continue
		}

		name := strings.TrimSuffix(parts[1], ".sql")

		if appliedVersions[version] {
			log.Printf("Migration %d already applied, skipping", version)
			continue
		}

		content, err := migrations.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration file %s: %w", entry.Name(), err)
		}

		log.Printf("Added migration %d (%s) to queue, SQL length: %d", version, name, len(content))

		toApply = append(toApply, Migration{
			Version: version,
			Name:    name,
			SQL:     string(content),
		})
	}

	sort.Slice(toApply, func(i, j int) bool {
		return toApply[i].Version < toApply[j].Version
	})

	return toApply, nil
}

// applyMigration runs one migration's SQL and records it in schema_migrations
// within a single transaction, so a failure rolls back both the schema change
// and its bookkeeping row (all-or-nothing).
func applyMigration(ctx context.Context, pool *pgxpool.Pool, migration Migration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func(tx pgx.Tx, ctx context.Context) {
		_ = tx.Rollback(ctx)
	}(tx, ctx)

	log.Printf("Executing migration SQL (length: %d bytes)", len(migration.SQL))

	if _, err := tx.Exec(ctx, migration.SQL); err != nil {
		return fmt.Errorf("execute migration SQL: %w", err)
	}

	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
		migration.Version, migration.Name,
	); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	return nil
}
