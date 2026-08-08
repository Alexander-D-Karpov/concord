// Package migrations runs the embedded SQL schema migrations.
//
// The NNN_*.sql files are embedded with go:embed; Run applies any not-yet-applied
// migrations in ascending version order (parsed from the filename) and records
// them in an auto-created tracking table. Run holds a Postgres advisory lock for
// its duration, so concurrent callers (multiple API replicas, or parallel test
// binaries sharing a database) serialize instead of racing on DDL. Run is invoked
// on concord-api startup, so these files are the schema source of truth.
package migrations
