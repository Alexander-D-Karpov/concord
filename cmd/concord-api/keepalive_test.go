package main

import "testing"

// TestKeepaliveOptions asserts the server registers exactly the two keepalive
// options (ServerParameters + EnforcementPolicy). grpc.ServerOption is opaque, so
// this guards wiring/count rather than values.
func TestKeepaliveOptions(t *testing.T) {
	if n := len(keepaliveServerOptions()); n != 2 {
		t.Fatalf("expected 2 keepalive options, got %d", n)
	}
}
