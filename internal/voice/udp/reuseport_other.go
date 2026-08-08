//go:build !linux

package udp

import (
	"errors"
	"syscall"
)

// reusePortControl is the non-Linux stub: SO_REUSEPORT is unavailable, so it
// always fails, preventing single-port mode from binding.
func reusePortControl(_, _ string, _ syscall.RawConn) error {
	return errors.New("single-port UDP mode (SO_REUSEPORT) is only supported on Linux")
}

// reusePortSupported reports SO_REUSEPORT availability; always false off Linux.
func reusePortSupported() bool { return false }
