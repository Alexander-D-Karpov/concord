// Package events is the in-process publish/subscribe hub that fans real-time
// ServerEvents out to connected gRPC event streams.
//
// Hub tracks connected clients by user ID and room subscriptions; each client has
// a writePump goroutine draining its send channel. It is the single fan-out point
// for the whole system — services emit events here rather than writing to client
// streams directly. Delivery is best-effort: a full client channel drops the
// event, and AddClient returns nil once the hub is shutting down (callers must
// nil-check).
package events
