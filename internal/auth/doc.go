// Package auth implements user authentication: registration, password and OAuth
// login, and access/refresh token issuance, refresh, and revocation.
//
// Service holds the business logic; Handler adapts it to gRPC. Refresh tokens are
// persisted only as SHA-256 hashes and are single-use: RefreshToken revokes the
// presented token and issues a fresh pair. Password logins are throttled by a
// cache-backed LockoutManager (enabled when a cache is configured): repeated
// failures for an identifier trip a temporary lockout that rejects further attempts
// with a too-many-requests error. Token minting and validation live in the auth/jwt
// subpackage (which also mints the voice tokens used by the voice subsystem); OAuth
// provider flows in auth/oauth.
package auth
