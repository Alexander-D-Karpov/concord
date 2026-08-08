// Package udp is the voice data plane: it binds UDP sockets, reads packets into a
// pooled buffer, and dispatches every voice packet type through a shared Handler.
//
// ServerPool is the production path (single-port SO_REUSEPORT or per-port fan-out)
// and wires the congestion controller; the single-socket Server is a legacy path
// built with a nil controller, so congestion logic is disabled on it. Packet
// buffers are reference-counted (Retain/Release): a missed Release leaks and a
// double Release corrupts pool reuse. handleMedia binds sessions by source
// address, and address migration (tryMigrateByMedia) only rebinds after a
// decrypt-verify plus a per-session cooldown — security-critical, do not loosen.
package udp
