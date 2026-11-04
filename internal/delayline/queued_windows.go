//go:build windows

package delayline

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fionread is Winsock's FIONREAD, _IOR('f', 127, u_long). x/sys/windows does
// not export it.
const fionread = 0x4004667f

// readQueued reads one datagram that is ALREADY in the socket's receive queue,
// and reports false -- without waiting -- when there is none.
//
// Go's Windows sockets are overlapped, not non-blocking, so a bare recv would
// sleep on an empty queue. FIONREAD asks first: on a datagram socket it is the
// bytes waiting in the receive buffer, and when it is non-zero the Read below
// returns at once. A zero-length datagram counts as nothing waiting and ends
// the drain; the hub never forwards one, and the reader skips them anyway.
func readQueued(conn *net.UDPConn, buf []byte) (int, bool) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return 0, false
	}
	var pending, returned uint32
	var ierr error
	cerr := rc.Control(func(fd uintptr) {
		ierr = windows.WSAIoctl(windows.Handle(fd), fionread, nil, 0,
			(*byte)(unsafe.Pointer(&pending)), uint32(unsafe.Sizeof(pending)), &returned, nil, 0)
	})
	if cerr != nil || ierr != nil || pending == 0 {
		return 0, false
	}
	n, err := conn.Read(buf)
	if err != nil {
		return 0, false
	}
	return n, true
}
