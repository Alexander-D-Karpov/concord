// Package jwt mints and validates the three token classes Concord uses: access,
// refresh, and voice tokens.
//
// Manager signs access and refresh tokens with the main secret and voice tokens
// with a separate secret, and every validation enforces the expected TokenType.
// The two-secret, type-checked design is load-bearing: an access token cannot be
// replayed as a voice token and vice versa.
package jwt
