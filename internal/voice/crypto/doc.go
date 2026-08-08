// Package crypto provides AES-256-GCM media encryption with per-SSRC nonce
// derivation and replay protection.
//
// Each SSRC's nonce base is derived with HKDF from the room ID, key ID, and SSRC
// rather than trusting a client-shared base, which prevents cross-sender GCM nonce
// reuse — the server's derivation must match the client's byte for byte.
// DecryptSSRC runs the sliding-window replay check before decrypting, which is
// load-bearing because the UDP handler uses a successful decrypt to migrate an
// address binding; a replayed packet must be rejected.
package crypto
