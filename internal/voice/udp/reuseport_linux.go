//go:build linux

package udp

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// reusePortControl sets SO_REUSEADDR + SO_REUSEPORT so multiple sockets in this
// process can bind the same UDP port. The kernel load-balances datagrams across
// them by 4-tuple, so a connected-UDP client's flow stays on one socket and the
// reply-from-ingress-socket path remains correct.
func reusePortControl(_, _ string, c syscall.RawConn) error {
	var opErr error
	if err := c.Control(func(fd uintptr) {
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
			opErr = err
			return
		}
		opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}); err != nil {
		return err
	}
	return opErr
}

// reusePortSupported reports whether SO_REUSEPORT single-port mode is available;
// always true on Linux.
func reusePortSupported() bool { return true }
