// Package testutil provides shared helpers for Concord's integration and
// database-backed unit tests. The helpers connect to a real PostgreSQL instance
// and (optionally) a real Redis instance, configured through DB_* and REDIS_*
// environment variables with sensible localhost defaults, rather than mocking
// those dependencies.
//
// The two dependencies differ in how they behave when unavailable:
//
//   - PostgreSQL is required. GetDB (and Pool, which wraps it) creates a unique
//     per-process database, runs migrations once, and asserts success via
//     require.NoError — so if Postgres is unreachable the calling test fails
//     rather than being skipped.
//   - Redis is optional. GetCache/GetAside degrade gracefully, logging and
//     returning nil when Redis is unreachable so cache-agnostic tests can still
//     run; the MustCache/MustAside variants instead fail the test when Redis is
//     required but absent.
//
// State is shared once per test binary. The database is created lazily on the
// first GetDB call under a sync.Once and reused by every test in the process;
// Teardown (typically called from a TestMain) closes the pool and drops that
// database. CacheTeardown similarly closes the shared Redis connection.
package testutil
