// Package circuitbreaker implements a simple three-state (closed, open, half-open)
// circuit breaker that protects a downstream call.
//
// Call holds the breaker's lock for the entire duration of the wrapped function,
// so calls through a single breaker are fully serialized — do not share one
// breaker across independent high-concurrency calls that expect parallelism. The
// transition to half-open happens lazily inside Call once the timeout has elapsed.
package circuitbreaker
