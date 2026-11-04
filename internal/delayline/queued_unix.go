//go:build unix

package delayline

import (
	"errors"
	"net"
	"syscall"
)

// readQueued reads one datagram that is ALREADY in the socket's receive queue,
// and reports false -- without waiting -- when there is none.
//
// Go's network sockets are non-blocking underneath, so a recvfrom on the raw
// descriptor answers EAGAIN on an empty queue rather than sleeping, and the
// callback returning true tells the poller not to wait for more. The caller has
// cleared the read deadline: an expired one fails the read before it is tried.
func readQueued(conn *net.UDPConn, buf []byte) (int, bool) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return 0, false
	}
	var n int
	var rerr error
	cerr := rc.Read(func(fd uintptr) bool {
		for {
			n, _, rerr = syscall.Recvfrom(int(fd), buf, 0)
			if !errors.Is(rerr, syscall.EINTR) {
				return true
			}
		}
	})
	if cerr != nil || rerr != nil {
		return 0, false
	}
	return n, true
}
