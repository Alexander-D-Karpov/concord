//go:build linux

package telemetry

import (
	"os"
	"strconv"
	"strings"
)

// clkTck is _SC_CLK_TCK; 100 on typical Linux (jiffies per second).
const clkTck = 100.0

// processCPUSeconds returns cumulative process CPU time (user+system) in seconds
// from /proc/self/stat.
func processCPUSeconds() float64 {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	s := string(data)
	// Field 2 (comm) is parenthesized and may contain spaces or ')'; the stable
	// fields begin after the final ')'.
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+2 >= len(s) {
		return 0
	}
	fields := strings.Fields(s[idx+2:])
	// After comm: state=0 ppid=1 ... utime=11 stime=12
	if len(fields) < 13 {
		return 0
	}
	utime, _ := strconv.ParseFloat(fields[11], 64)
	stime, _ := strconv.ParseFloat(fields[12], 64)
	return (utime + stime) / clkTck
}
