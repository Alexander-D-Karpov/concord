// Command concord-api is the main Concord backend server.
//
// It runs a single gRPC server for auth, rooms, chat, DMs, friends, presence,
// admin, and message features, fronted by an HTTP/JSON gateway (grpc-gateway)
// with a Swagger UI, file serving, Prometheus metrics, and health endpoints.
// run() in main.go is the composition root: every repository, service, and
// handler is constructed and dependency-injected there, then registered on the
// server. It owns PostgreSQL (migrations run on startup) and optional Redis, and
// assigns clients to voice servers via the registry/voiceassign subsystem.
package main
