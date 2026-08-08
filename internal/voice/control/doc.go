// Package control is the voice node's local gRPC RegistryService server.
//
// It currently answers Heartbeat as a near no-op (debug log only). This is the
// inbound direction; the outbound registration and heartbeat flow to the main API
// lives in internal/voice/discovery — do not confuse the two.
package control
