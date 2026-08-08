// Package telemetry provides the voice server's metrics and operational logging:
// atomic counters, Prometheus/JSON exposition, interval-rate snapshots, and
// CPU/egress load sampling.
//
// The CPU source is platform-split (load_linux.go vs load_other.go). A LoadSampler
// returns zeros on its first Sample because it establishes the delta baseline, so
// the first CPU/Mbps reading is 0; main keeps two independent samplers so they do
// not clobber each other's deltas. Room-label cardinality is bounded by exporting
// only the top-N rooms.
package telemetry
