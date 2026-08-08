// Package gateway runs the grpc-gateway HTTP/JSON reverse proxy that fronts the
// gRPC server, adding CORS, request logging, and version headers.
//
// New configures the proxy; Init must be called before Start because it dials the
// gRPC backend to register the service handlers. customMatcher controls which
// inbound HTTP headers are forwarded as gRPC metadata.
package gateway
