// Package auth validates voice tokens presented during the UDP voice handshake.
//
// Validator wraps jwt.Manager and checks the voice-token class (not access tokens)
// so the media plane can authenticate a client's Hello before creating a session.
package auth
