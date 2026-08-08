// Package cache wraps the Redis client and provides a cache-aside pattern with
// hit/miss metrics and a profile warmer.
//
// AsidePattern.GetOrLoad reads through to a loader on a miss, but it does not
// dedupe concurrent loads (a thundering herd is possible) and it ignores Set
// errors. A miss is reported as ErrCacheMiss, so callers must compare with
// errors.Is. DeletePattern uses a non-atomic Redis SCAN + pipelined DEL.
package cache
