package migrations

import (
	"context"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"

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

	for _, migration := range migrationsToApply {
		if err := applyMigration(ctx, pool, migration); err != nil {
			return fmt.Errorf("apply migration %d: %w", migration.Version, err)
		}
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
		return nil, err
	}

	var toApply []Migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		var version int
		var name string
		_, err := fmt.Sscanf(entry.Name(), "%d_%s.sql", &version, &name)
		if err != nil {
			continue
		}

		if appliedVersions[version] {
			continue
		}

		content, err := migrations.ReadFile(entry.Name())
		if err != nil {
			return nil, err
		}

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
		return err
	}
	defer func(tx pgx.Tx, ctx context.Context) {
		err := tx.Rollback(ctx)
		if err != nil {
			return
		}
	}(tx, ctx)

	if _, err := tx.Exec(ctx, migration.SQL); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
		migration.Version, migration.Name,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
