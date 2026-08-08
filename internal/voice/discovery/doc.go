// Package discovery is the client side of voice-server registration: it dials the
// main API's RegistryService, registers this server, and runs the heartbeat loop.
//
// It uses two distinct secrets — registerSecret on the registration call and
// serverSecret (plus the server ID) on heartbeats — sent as gRPC metadata. After
// three consecutive heartbeat failures it re-registers. The metadata key literals
// are duplicated from the API side and kept in sync by a test.
package discovery
