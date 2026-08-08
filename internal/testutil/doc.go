// Package testutil provides helpers for database-backed tests: a pool, table
// truncation, and fixture seeders (SeedUser, SeedRoom, SeedMembership,
// SeedDMChannel).
//
// It connects to a real Postgres derived from test environment variables, so tests
// that import it require a live database (Pool skips the test when Postgres is
// unreachable). Each test process gets its own freshly-created, migrated database
// (concord_test_<pid>_<n>), so packages running in parallel never race on shared
// tables — one package's Truncate cannot affect another. Drop the per-run
// databases with `make test-cleanup`. Seeders take *testing.T and fail fast.
package testutil
