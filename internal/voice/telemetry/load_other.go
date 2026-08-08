//go:build !linux

package telemetry

// processCPUSeconds is a no-op off Linux (heartbeat CPU reported as 0).
func processCPUSeconds() float64 { return 0 }
