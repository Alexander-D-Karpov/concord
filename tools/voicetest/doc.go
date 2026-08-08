// Command voicetest is the voice throughput/stress harness for concord-voice.
//
// It lives in a separate Go module (wired via go.work) and spins up N simulated
// UDP voice bots that log in over gRPC, join a room, and send/receive synthetic
// audio and video, optionally under network impairment (loss/jitter/reorder) and
// churn (leave-rejoin, socket rebind) to exercise session migration. It renders a
// live TUI or a headless frame dump. The default -fast-join requires the server to
// run with VOICE_DEBUG=true; key env overrides are GRPC_API_URL, USE_TLS, and
// RATE_LIMIT_BYPASS_TOKEN.
package main
