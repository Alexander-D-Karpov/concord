// Package retry provides a context-aware exponential-backoff retry helper with
// jitter.
//
// WithBackoff retries on any non-nil error — there is no retryable-error
// predicate — so wrap only idempotent work with it. The context is honored during
// the backoff wait but not during the wrapped function's execution.
package retry
