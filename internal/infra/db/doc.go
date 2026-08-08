// Package db wraps the pgx connection pool with retry, pool monitoring, and
// slow-query tracing.
//
// New builds a *DB from config; WithRetry re-runs an operation on transient
// failures classified by isRetriable, using exponential backoff. PoolMonitor logs
// pool saturation periodically and SlowQueryLogger (a pgx QueryTracer) logs
// queries slower than a threshold.
package db
