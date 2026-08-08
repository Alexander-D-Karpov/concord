// Package security provides TLS configuration helpers.
//
// LoadTLSConfig builds a mutual-TLS config with a CA pool for verifying peer
// certificates; ServerTLSConfig loads only a server certificate with no peer
// verification. Choose the constructor that matches the trust direction you need.
package security
