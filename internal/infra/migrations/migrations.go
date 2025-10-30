package migrations

import (
	"context"
	"embed"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var migrations embed.FS

type Migration struct {
	Version int
	Name    string
	SQL     string
}

func Run(ctx context.Context, pool *pgxpool.Pool) error {
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

		var version int
		var name string
		n, err := fmt.Sscanf(entry.Name(), "%d_%s.sql", &version, &name)
		if err != nil || n != 2 {
			log.Printf("Skipping file %s: failed to parse (scanned %d fields, err: %v)", entry.Name(), n, err)
			continue
		}

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
