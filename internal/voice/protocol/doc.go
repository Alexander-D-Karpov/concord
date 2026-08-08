// Package protocol defines the voice server's on-the-wire format: packet-type
// constants, the binary media and fragment headers, and the JSON control payloads
// with their build/parse helpers.
//
// The current ProtocolVersion is 3; the Layer byte in the 24-byte media header
// (Layer at offset 22, offset 23 reserved) and the BitrateHint control packet are
// its additions. Protocol is negotiated down to what the client speaks, because a
// strict v2 client discards an unsolicited v3 Welcome and re-Hellos forever. Media
// dispatch keys off the first byte; parse helpers are length-checked and return
// ErrTooSmall on a short buffer (ErrInvalidPacket is declared but not currently
// returned by them).
package protocol
