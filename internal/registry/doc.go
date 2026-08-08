// Package registry is the API-side directory of voice/media servers.
//
// Voice servers register and heartbeat through it; Service persists them
// (secrets stored hashed via HashSecret) and ranks them with calculateLoadScore
// for assignment. Machine-to-machine registry RPCs are authenticated separately
// from user JWTs by MachineAuthInterceptor, gated on IsMachineMethod. The
// outbound client side of this protocol lives in internal/voice/discovery.
package registry
