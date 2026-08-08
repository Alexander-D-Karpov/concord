// Package health serves the voice server's HTTP liveness/readiness endpoint,
// aggregating named check functions.
//
// Any single failing check flips the overall status to unhealthy (HTTP 503). This
// is distinct from the status package's JSON API and from telemetry's /metrics.
package health
