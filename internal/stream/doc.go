// Package stream implements StreamService.EventStream, the long-lived gRPC stream
// that wires a connected client into the events Hub.
//
// The handler marks the user online, registers the client with the Hub, and (when
// a sender is set) pushes a voice-state snapshot on connect. It reads inbound
// client events but only acts on Ack (treated as a presence heartbeat); other
// payloads are ignored. On disconnect it removes the client and marks the user
// offline. The VoiceSnapshotSender is injected after construction
// (SetVoiceSnapshotSender) to break an import cycle with the voice subsystem.
package stream
