// Command concord-voice is the Concord media (voice/video) server.
//
// It is a stateless UDP SFU (Selective Forwarding Unit) that relays encrypted
// audio/video between the peers in a room. It holds no database: on startup it
// registers with concord-api over the registry gRPC service and heartbeats every
// 30s. run() in main.go wires the live path — session.Manager, congestion
// controller, router, and the UDP server pool — plus a TCP/TLS fallback,
// control/status/health servers, and telemetry. All session state is in memory
// and lost on restart.
package main
