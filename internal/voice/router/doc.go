// Package router is the SFU forwarding engine that fans media and control packets
// out to the other peers in a room.
//
// It runs N worker goroutines (NumCPU*2, clamped 4..32), each with strict
// control > audio > video priority queues; a destination SSRC is hashed to one
// worker so its egress is single-threaded. Media older than ~80ms is dropped at
// send time to shed backlog, while control is never aged out. SetDefaultConn must
// be called once at startup — it is the fallback egress socket for TCP-origin
// media whose packet has no UDP connection.
package router
